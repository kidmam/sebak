package sync

import (
	"context"
	"strconv"
	"time"

	"boscoin.io/sebak/lib/block"
	"boscoin.io/sebak/lib/common"
	"boscoin.io/sebak/lib/common/observer"
	"boscoin.io/sebak/lib/network"
	"boscoin.io/sebak/lib/node"
	"boscoin.io/sebak/lib/storage"
	"github.com/inconshreveable/log15"
)

type requestHighestBlock struct {
	height    uint64
	nodeAddrs []string
}

type Syncer struct {
	poolSize      uint64
	fetchTimeout  time.Duration
	retryInterval time.Duration
	checkInterval time.Duration

	afterFunc AfterFunc

	storage           *storage.LevelDBBackend
	network           network.Network
	connectionManager network.ConnectionManager
	networkID         []byte
	commonCfg         common.Config
	localNode         *node.LocalNode

	fetcher   Fetcher
	validator Validator

	workPool   *Pool
	stop       chan chan struct{}
	ctx        context.Context
	cancelFunc context.CancelFunc

	requestHighestBlock chan *requestHighestBlock
	getSyncProgress     chan chan *SyncProgress

	logger log15.Logger
}

type SyncerOption func(s *Syncer)

func NewSyncer(st *storage.LevelDBBackend,
	nw network.Network,
	cm network.ConnectionManager,
	networkID []byte,
	localNode *node.LocalNode,
	cfg common.Config,
	opts ...SyncerOption) *Syncer {
	ctx, cancelFunc := context.WithCancel(context.Background())

	s := &Syncer{
		storage:           st,
		network:           nw,
		connectionManager: cm,
		networkID:         networkID,
		commonCfg:         cfg,
		localNode:         localNode,

		poolSize:      SyncPoolSize,
		fetchTimeout:  FetchTimeout,
		retryInterval: RetryInterval,
		checkInterval: CheckBlockHeightInterval,

		afterFunc: time.After,

		stop:       make(chan chan struct{}),
		ctx:        ctx,
		cancelFunc: cancelFunc,

		requestHighestBlock: make(chan *requestHighestBlock),
		getSyncProgress:     make(chan chan *SyncProgress),

		logger: common.NopLogger(),
	}

	for _, opt := range opts {
		opt(s)
	}

	fetcher := NewBlockFetcher(nw, cm, st, localNode, func(f *BlockFetcher) {
		f.fetchTimeout = s.fetchTimeout
		f.logger = s.logger
	})
	s.fetcher = fetcher

	validator := NewBlockValidator(nw, st, networkID, cfg, func(v *BlockValidator) {
		v.logger = s.logger
	})
	s.validator = validator

	return s
}

func (s *Syncer) Stop() error {
	s.cancelFunc()
	c := make(chan struct{})
	s.stop <- c
	<-c
	s.workPool.Finish()
	s.logger.Info("stopped syncer")
	return nil
}

func (s *Syncer) Start() error {
	s.logger.Info("starting syncer")
	s.workPool = NewPool(s.poolSize)
	s.loop()
	return nil
}

func (s *Syncer) SetSyncTargetBlock(ctx context.Context, height uint64, nodeAddrs []string) error {
	s.logger.Info("sync start")
	nas := make([]string, len(nodeAddrs))
	copy(nas, nodeAddrs) // preventing data race
	req := &requestHighestBlock{
		height:    height,
		nodeAddrs: nas,
	}
	select {
	case s.requestHighestBlock <- req:
		s.logger.Debug("SetSyncTargetBlock", "req", req)
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

func (s *Syncer) SyncProgress(ctx context.Context) (*SyncProgress, error) {
	c := make(chan *SyncProgress, 1)
	select {
	case s.getSyncProgress <- c:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case sp := <-c:
		return sp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Syncer) loop() {
	var (
		checkc       = s.afterFunc(s.checkInterval)
		notifyc      = make(chan struct{})
		onNotify     = false
		height       = s.latestBlockHeight()
		syncProgress = &SyncProgress{
			StartingBlock: height,
			CurrentBlock:  height,
			HighestBlock:  height,
		}
		nodeAddrs []string
	)

	for {
		select {
		case <-checkc:
			s.logger.Debug("check interval", "checkInterval", s.checkInterval)
			s.sync(syncProgress, nodeAddrs)
			checkc = s.afterFunc(s.checkInterval)
		case <-notifyc:
			s.logger.Debug("got notification finished height")
			onNotify = false // reset onNotify for singleflight
			s.sync(syncProgress, nodeAddrs)
		case req := <-s.requestHighestBlock:
			height := req.height
			nodeAddrs = req.nodeAddrs
			s.logger.Info("updated highest height", "height", height, "nodes", len(nodeAddrs))
			if height > syncProgress.CurrentBlock {
				syncProgress.HighestBlock = height
				s.sync(syncProgress, nodeAddrs)
			}
		case c := <-s.getSyncProgress:
			c <- syncProgress
		case c := <-s.stop:
			close(c)
			return
		}

		if !onNotify && syncProgress.CurrentBlock < syncProgress.HighestBlock {
			event := strconv.FormatUint(syncProgress.CurrentBlock, 10)
			observer.SyncBlockWaitObserver.One(event, func(...interface{}) {
				select {
				case notifyc <- struct{}{}:
				case <-s.ctx.Done():
				}
				s.logger.Debug("send for notify", "event", event)
			})
			onNotify = true
		}
	}
}

func (s *Syncer) sync(p *SyncProgress, nodeAddrs []string) {
	var (
		startHeight       = p.CurrentBlock + 1
		currentHeight     = p.CurrentBlock
		highestHeight     = p.HighestBlock
		latestBlockHeight = s.latestBlockHeight()
	)

	if latestBlockHeight > currentHeight {
		startHeight = latestBlockHeight + 1
	}
	if startHeight > highestHeight {
		p.StartingBlock = latestBlockHeight + 1
		p.CurrentBlock = currentHeight

		logmsg := "sync progress skip: start height is over or equal than highest (requested) height"
		s.logger.Debug(logmsg,
			"start", p.StartingBlock, "cur", p.CurrentBlock, "high", p.HighestBlock)
		return
	}

	for height := startHeight; height <= highestHeight; height++ {
		s.logger.Debug("work height", "height", height)
		// TryAdd for unblocking when the pool is full. Just keep syncprogress for next sync
		if s.work(height, nodeAddrs) == false {
			break
		}
		currentHeight = height
	}
	p.StartingBlock = startHeight
	p.CurrentBlock = currentHeight

	s.logger.Info("sync progress",
		"start", p.StartingBlock, "cur", p.CurrentBlock, "high", p.HighestBlock)
}

func (s *Syncer) work(height uint64, nodeAddrs []string) bool {
	ctx := s.ctx
	work := func() {
		latestHeight := s.latestBlockHeight()
		if latestHeight > 0 && height <= latestHeight {
			s.logger.Info("this height has already synced", "height", height)
			return
		}

		var (
			syncInfo = &SyncInfo{
				Height:    height,
				NodeAddrs: nodeAddrs,
			}
			err error
		)

	L:
		for {
			select {
			case <-ctx.Done():
				break L
			default:
				syncInfo, err = s.fetcher.Fetch(ctx, syncInfo)
				if err != nil {
					if err != context.Canceled {
						s.logger.Error("fetch failure", "err", err, "height", height)
					}
				}
				err = s.validator.Validate(ctx, syncInfo)
				if err != nil {
					if err != context.Canceled {
						s.logger.Error("validate failure", "err", err, "height", height)
					}
					continue
				}
				break L
			}
		}
		if err != nil {
			if err != context.Canceled {
				s.logger.Error("stop sync work", "height", height, "err", err)
			} else {
				s.logger.Debug("stop sync work", "height", height, "err", err)
			}
		} else {
			s.logger.Info("done sync work", "height", height, "hash", syncInfo.Block.Hash)
		}
	}
	return s.workPool.TryAdd(ctx, work)
}

func (s *Syncer) latestBlockHeight() uint64 {
	blk := block.GetLatestBlock(s.storage)
	return blk.Height
}
