package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	logging "github.com/inconshreveable/log15"
	"github.com/oklog/run"
	"github.com/spf13/cobra"
	"github.com/stellar/go/keypair"
	"github.com/ulule/limiter"
	"golang.org/x/net/http2"

	cmdcommon "boscoin.io/sebak/cmd/sebak/common"
	"boscoin.io/sebak/lib/common"
	"boscoin.io/sebak/lib/consensus"
	"boscoin.io/sebak/lib/errors"
	"boscoin.io/sebak/lib/network"
	"boscoin.io/sebak/lib/node"
	"boscoin.io/sebak/lib/node/runner"
	"boscoin.io/sebak/lib/storage"
	"boscoin.io/sebak/lib/sync"
)

const (
	defaultBindURL   string      = "https://0.0.0.0:12345"
	defaultLogFormat string      = "terminal"
	defaultLogLevel  logging.Lvl = logging.LvlInfo
)

var (
	flagBindURL           string = common.GetENVValue("SEBAK_BIND", defaultBindURL)
	flagBlockTime         string = common.GetENVValue("SEBAK_BLOCK_TIME", "5")
	flagDebugPProf        bool   = common.GetENVValue("SEBAK_DEBUG_PPROF", "0") == "1"
	flagKPSecretSeed      string = common.GetENVValue("SEBAK_SECRET_SEED", "")
	flagLog               string = common.GetENVValue("SEBAK_LOG", "")
	flagLogLevel          string = common.GetENVValue("SEBAK_LOG_LEVEL", defaultLogLevel.String())
	flagLogFormat         string = common.GetENVValue("SEBAK_LOG_FORMAT", defaultLogFormat)
	flagNetworkID         string = common.GetENVValue("SEBAK_NETWORK_ID", "")
	flagOperationsLimit   string = common.GetENVValue("SEBAK_OPERATIONS_LIMIT", "1000")
	flagPublishURL        string = common.GetENVValue("SEBAK_PUBLISH", "")
	flagSyncCheckInterval string = common.GetENVValue("SEBAK_SYNC_CHECK_INTERVAL", "30s")
	flagSyncFetchTimeout  string = common.GetENVValue("SEBAK_SYNC_FETCH_TIMEOUT", "1m")
	flagSyncPoolSize      string = common.GetENVValue("SEBAK_SYNC_POOL_SIZE", "300")
	flagSyncRetryInterval string = common.GetENVValue("SEBAK_SYNC_RETRY_INTERVAL", "10s")
	flagThreshold         string = common.GetENVValue("SEBAK_THRESHOLD", "67")
	flagTimeoutACCEPT     string = common.GetENVValue("SEBAK_TIMEOUT_ACCEPT", "2")
	flagTimeoutINIT       string = common.GetENVValue("SEBAK_TIMEOUT_INIT", "2")
	flagTimeoutSIGN       string = common.GetENVValue("SEBAK_TIMEOUT_SIGN", "2")
	flagTLSCertFile       string = common.GetENVValue("SEBAK_TLS_CERT", "sebak.crt")
	flagTLSKeyFile        string = common.GetENVValue("SEBAK_TLS_KEY", "sebak.key")
	flagTransactionsLimit string = common.GetENVValue("SEBAK_TRANSACTIONS_LIMIT", "1000")
	flagUnfreezingPeriod  string = common.GetENVValue("SEBAK_UNFREEZING_PERIOD", "241920")
	flagValidators        string = common.GetENVValue("SEBAK_VALIDATORS", "")
	flagVerbose           bool   = common.GetENVValue("SEBAK_VERBOSE", "0") == "1"

	flagRateLimitAPI        cmdcommon.ListFlags // "SEBAK_RATE_LIMIT_API"
	flagRateLimitNode       cmdcommon.ListFlags // "SEBAK_RATE_LIMIT_NODE"
	flagStorageConfigString string
)

var (
	nodeCmd *cobra.Command

	bindEndpoint      *common.Endpoint
	blockTime         time.Duration
	kp                *keypair.Full
	localNode         *node.LocalNode
	operationsLimit   uint64
	publishEndpoint   *common.Endpoint
	rateLimitRuleAPI  common.RateLimitRule
	rateLimitRuleNode common.RateLimitRule
	storageConfig     *storage.Config
	syncCheckInterval time.Duration
	syncFetchTimeout  time.Duration
	syncPoolSize      uint64
	syncRetryInterval time.Duration
	threshold         int
	timeoutACCEPT     time.Duration
	timeoutINIT       time.Duration
	timeoutSIGN       time.Duration
	transactionsLimit uint64
	validators        []*node.Validator

	logLevel logging.Lvl
	log      logging.Logger = logging.New("module", "main")
)

func init() {
	var err error
	var flagGenesis string

	nodeCmd = &cobra.Command{
		Use:   "node",
		Short: "Run sebak node",
		Run: func(c *cobra.Command, args []string) {
			// If `--genesis` was provided, perfom `sebak genesis` before starting the node
			// This allows one-step startup from scratch, quite useful for testing
			if len(flagGenesis) != 0 {
				var balanceStr string
				csv := strings.Split(flagGenesis, ",")
				if len(csv) < 2 || len(csv) > 3 {
					cmdcommon.PrintFlagsError(nodeCmd, "--genesis",
						errors.New("--genesis expects '<genesis address>,<common account>[,balance]"))
				}
				if len(csv) == 3 {
					balanceStr = csv[1]
				}
				flagName, err := makeGenesisBlock(csv[0], csv[1], flagNetworkID, balanceStr, flagStorageConfigString, log)
				if len(flagName) != 0 || err != nil {
					cmdcommon.PrintFlagsError(c, flagName, err)
				}
			}

			parseFlagsNode()

			if err = runNode(); err != nil {
				// TODO: Handle errors here
				// We should handle the error correctly, however since we don't currently
				// shut down the HTTP server correctly, we get an error,
				// and we need the binary to exit with a successfull error code for
				// code coverage in integration test to work.
				log.Error("Node exited with error: ", err)
			}
		},
	}

	// storage
	var currentDirectory string
	if currentDirectory, err = os.Getwd(); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--storage", err)
	}
	if currentDirectory, err = filepath.Abs(currentDirectory); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--storage", err)
	}
	flagStorageConfigString = common.GetENVValue("SEBAK_STORAGE", fmt.Sprintf("file://%s/db", currentDirectory))

	nodeCmd.Flags().StringVar(&flagGenesis, "genesis", flagGenesis, "performs the 'genesis' command before running node. Syntax: key[,balance]")
	nodeCmd.Flags().StringVar(&flagKPSecretSeed, "secret-seed", flagKPSecretSeed, "secret seed of this node")
	nodeCmd.Flags().StringVar(&flagNetworkID, "network-id", flagNetworkID, "network id")
	nodeCmd.Flags().StringVar(&flagLogLevel, "log-level", flagLogLevel, "log level, {crit, error, warn, info, debug}")
	nodeCmd.Flags().StringVar(&flagLogFormat, "log-format", flagLogFormat, "log format, {terminal, json}")
	nodeCmd.Flags().StringVar(&flagLog, "log", flagLog, "set log file")
	nodeCmd.Flags().BoolVar(&flagVerbose, "verbose", flagVerbose, "verbose")
	nodeCmd.Flags().StringVar(&flagBindURL, "bind", flagBindURL, "bind to listen on")
	nodeCmd.Flags().StringVar(&flagPublishURL, "publish", flagPublishURL, "endpoint url for other nodes")
	nodeCmd.Flags().StringVar(&flagStorageConfigString, "storage", flagStorageConfigString, "storage uri")
	nodeCmd.Flags().StringVar(&flagTLSCertFile, "tls-cert", flagTLSCertFile, "tls certificate file")
	nodeCmd.Flags().StringVar(&flagTLSKeyFile, "tls-key", flagTLSKeyFile, "tls key file")
	nodeCmd.Flags().StringVar(&flagValidators, "validators", flagValidators, "set validator: <endpoint url>?address=<public address>[&alias=<alias>] [ <validator>...]")
	nodeCmd.Flags().StringVar(&flagThreshold, "threshold", flagThreshold, "threshold")
	nodeCmd.Flags().StringVar(&flagTimeoutINIT, "timeout-init", flagTimeoutINIT, "timeout of the init state")
	nodeCmd.Flags().StringVar(&flagTimeoutSIGN, "timeout-sign", flagTimeoutSIGN, "timeout of the sign state")
	nodeCmd.Flags().StringVar(&flagTimeoutACCEPT, "timeout-accept", flagTimeoutACCEPT, "timeout of the accept state")
	nodeCmd.Flags().StringVar(&flagBlockTime, "block-time", flagBlockTime, "block creation time")
	nodeCmd.Flags().StringVar(&flagTransactionsLimit, "transactions-limit", flagTransactionsLimit, "transactions limit in a ballot")
	nodeCmd.Flags().StringVar(&flagUnfreezingPeriod, "unfreezing-period", flagUnfreezingPeriod, "how long freezing must last")
	nodeCmd.Flags().StringVar(&flagOperationsLimit, "operations-limit", flagOperationsLimit, "operations limit in a transaction")
	nodeCmd.Flags().Var(
		&flagRateLimitAPI,
		"rate-limit-api",
		fmt.Sprintf("rate limit for %s: [<ip>=]<limit>-<period>, ex) '10-S' '3.3.3.3=1000-M'", network.UrlPathPrefixAPI),
	)
	nodeCmd.Flags().Var(
		&flagRateLimitNode,
		"rate-limit-node",
		fmt.Sprintf("rate limit for %s: [<ip>=]<limit>-<period>, ex) '10-S' '3.3.3.3=1000-M'", network.UrlPathPrefixNode),
	)
	nodeCmd.Flags().BoolVar(&flagDebugPProf, "debug-pprof", flagDebugPProf, "set debug pprof")
	nodeCmd.Flags().StringVar(&flagSyncPoolSize, "sync-pool-size", flagSyncPoolSize, "sync pool size")
	nodeCmd.Flags().StringVar(&flagSyncFetchTimeout, "sync-fetch-timeout", flagSyncFetchTimeout, "sync fetch timeout")
	nodeCmd.Flags().StringVar(&flagSyncRetryInterval, "sync-retry-interval", flagSyncRetryInterval, "sync retry interval")
	nodeCmd.Flags().StringVar(&flagSyncCheckInterval, "sync-check-interval", flagSyncCheckInterval, "sync check interval")

	rootCmd.AddCommand(nodeCmd)
}

func parseFlagRateLimit(l cmdcommon.ListFlags, defaultRate limiter.Rate) (rule common.RateLimitRule, err error) {
	if len(l) < 1 {
		rule = common.NewRateLimitRule(defaultRate)
		return
	}

	var givenRate limiter.Rate

	byIPAddress := map[string]limiter.Rate{}
	for _, s := range l {
		sl := strings.SplitN(s, "=", 2)

		var ip, r string
		if len(sl) < 2 {
			r = s
		} else {
			ip = sl[0]
			r = sl[1]
		}

		if len(ip) > 0 {
			if net.ParseIP(ip) == nil {
				err = fmt.Errorf("invalid ip address")
				return
			}
		}

		var rate limiter.Rate
		if rate, err = limiter.NewRateFromFormatted(r); err != nil {
			return
		}

		if len(ip) > 0 {
			byIPAddress[ip] = rate
		} else {
			givenRate = rate
		}
	}

	if givenRate.Period < 1 && givenRate.Limit < 1 {
		givenRate = defaultRate
	}

	rule = common.NewRateLimitRule(givenRate)
	rule.ByIPAddress = byIPAddress

	return
}

func parseFlagValidators(v string) (vs []*node.Validator, err error) {
	splitted := strings.Fields(strings.TrimSpace(v))
	if len(splitted) < 1 {
		err = fmt.Errorf("must be given")
		return
	}

	for _, v := range splitted {
		if v == "self" {
			continue
		}

		var validator *node.Validator
		if validator, err = node.NewValidatorFromURI(v); err != nil {
			return
		}
		vs = append(vs, validator)
	}

	return
}

func parseFlagsNode() {
	var err error

	if len(flagNetworkID) < 1 {
		cmdcommon.PrintFlagsError(nodeCmd, "--network-id", errors.New("--network-id must be given"))
	}
	if len(flagKPSecretSeed) < 1 {
		cmdcommon.PrintFlagsError(nodeCmd, "--secret-seed", errors.New("must be given"))
	}

	var parsedKP keypair.KP
	parsedKP, err = keypair.Parse(flagKPSecretSeed)
	if err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--secret-seed", err)
	} else {
		kp = parsedKP.(*keypair.Full)
	}

	if p, err := common.ParseEndpoint(flagBindURL); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--bind", err)
	} else {
		bindEndpoint = p
		flagBindURL = bindEndpoint.String()
	}

	if strings.ToLower(bindEndpoint.Scheme) == "https" {
		if _, err = os.Stat(flagTLSCertFile); os.IsNotExist(err) {
			cmdcommon.PrintFlagsError(nodeCmd, "--tls-cert", err)
		}
		if _, err = os.Stat(flagTLSKeyFile); os.IsNotExist(err) {
			cmdcommon.PrintFlagsError(nodeCmd, "--tls-key", err)
		}
	}

	queries := bindEndpoint.Query()
	queries.Add("TLSCertFile", flagTLSCertFile)
	queries.Add("TLSKeyFile", flagTLSKeyFile)
	queries.Add("IdleTimeout", "3s")
	bindEndpoint.RawQuery = queries.Encode()

	if len(flagPublishURL) > 0 {
		if p, err := common.ParseEndpoint(flagPublishURL); err != nil {
			cmdcommon.PrintFlagsError(nodeCmd, "--publish", err)
		} else {
			publishEndpoint = p
			flagPublishURL = publishEndpoint.String()
		}
	} else {
		publishEndpoint = &common.Endpoint{}
		*publishEndpoint = *bindEndpoint
		publishEndpoint.Host = fmt.Sprintf("localhost:%s", publishEndpoint.Port())
		flagPublishURL = publishEndpoint.String()
	}

	if validators, err = parseFlagValidators(flagValidators); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--validators", err)
	}

	if storageConfig, err = storage.NewConfigFromString(flagStorageConfigString); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--storage", err)
	}

	timeoutINIT = getTime(flagTimeoutINIT, 2*time.Second, "--timeout-init")
	timeoutSIGN = getTime(flagTimeoutSIGN, 2*time.Second, "--timeout-sign")
	timeoutACCEPT = getTime(flagTimeoutACCEPT, 2*time.Second, "--timeout-accept")
	blockTime = getTime(flagBlockTime, 5*time.Second, "--block-time")

	if transactionsLimit, err = strconv.ParseUint(flagTransactionsLimit, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--transactions-limit", err)
	}

	if operationsLimit, err = strconv.ParseUint(flagOperationsLimit, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--operations-limit", err)
	}

	var tmpUint64 uint64
	if tmpUint64, err = strconv.ParseUint(flagThreshold, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--threshold", err)
	} else {
		threshold = int(tmpUint64)
	}

	if common.UnfreezingPeriod, err = strconv.ParseUint(flagUnfreezingPeriod, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--unfreezing-period", err)
	}

	if syncPoolSize, err = strconv.ParseUint(flagSyncPoolSize, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--sync-pool-size", err)
	}

	syncRetryInterval = getTimeDuration(flagSyncRetryInterval, sync.RetryInterval, "--sync-retry-interval")
	syncFetchTimeout = getTimeDuration(flagSyncFetchTimeout, sync.FetchTimeout, "--sync-fetch-timeout")
	syncCheckInterval = getTimeDuration(flagSyncCheckInterval, sync.CheckBlockHeightInterval, "--sync-check-interval")

	if logLevel, err = logging.LvlFromString(flagLogLevel); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--log-level", err)
	}

	var logFormatter logging.Format
	switch flagLogFormat {
	case "terminal":
		logFormatter = logging.TerminalFormat()
	case "json":
		logFormatter = common.JsonFormatEx(false, true)
	default:
		cmdcommon.PrintFlagsError(nodeCmd, "--log-format", fmt.Errorf("'%s'", flagLogFormat))
	}

	logHandler := logging.StreamHandler(os.Stdout, logFormatter)
	if len(flagLog) > 0 {
		if logHandler, err = logging.FileHandler(flagLog, logFormatter); err != nil {
			cmdcommon.PrintFlagsError(nodeCmd, "--log", err)
		}
	}

	logHandler = logging.CallerFileHandler(logHandler)
	logHandler = logging.LvlFilterHandler(logLevel, logHandler)
	log.SetHandler(logHandler)

	runner.SetLogging(logLevel, logHandler)
	consensus.SetLogging(logLevel, logHandler)
	network.SetLogging(logLevel, logHandler)
	sync.SetLogging(logLevel, logHandler)

	if len(flagRateLimitAPI) < 1 {
		re := strings.Fields(common.GetENVValue("SEBAK_RATE_LIMIT_API", ""))
		for _, r := range re {
			flagRateLimitAPI.Set(r)
		}
	}

	rateLimitRuleAPI, err = parseFlagRateLimit(flagRateLimitAPI, common.RateLimitAPI)
	if err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--rate-limit-api", err)
	}

	if len(flagRateLimitNode) < 1 {
		re := strings.Fields(common.GetENVValue("SEBAK_RATE_LIMIT_NODE", ""))
		for _, r := range re {
			flagRateLimitNode.Set(r)
		}
	}
	rateLimitRuleNode, err = parseFlagRateLimit(flagRateLimitNode, common.RateLimitNode)
	if err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, "--rate-limit-node", err)
	}

	log.Info("Starting Sebak")

	// print flags
	parsedFlags := []interface{}{}
	parsedFlags = append(parsedFlags, "\n\tnetwork-id", flagNetworkID)
	parsedFlags = append(parsedFlags, "\n\tbind", flagBindURL)
	parsedFlags = append(parsedFlags, "\n\tpublish", flagPublishURL)
	parsedFlags = append(parsedFlags, "\n\tstorage", flagStorageConfigString)
	parsedFlags = append(parsedFlags, "\n\ttls-cert", flagTLSCertFile)
	parsedFlags = append(parsedFlags, "\n\ttls-key", flagTLSKeyFile)
	parsedFlags = append(parsedFlags, "\n\tlog-level", flagLogLevel)
	parsedFlags = append(parsedFlags, "\n\tlog-format", flagLogFormat)
	parsedFlags = append(parsedFlags, "\n\tlog", flagLog)
	parsedFlags = append(parsedFlags, "\n\tthreshold", flagThreshold)
	parsedFlags = append(parsedFlags, "\n\ttimeout-init", flagTimeoutINIT)
	parsedFlags = append(parsedFlags, "\n\ttimeout-sign", flagTimeoutSIGN)
	parsedFlags = append(parsedFlags, "\n\ttimeout-accept", flagTimeoutACCEPT)
	parsedFlags = append(parsedFlags, "\n\tblock-time", flagBlockTime)
	parsedFlags = append(parsedFlags, "\n\ttransactions-limit", flagTransactionsLimit)
	parsedFlags = append(parsedFlags, "\n\toperations-limit", flagOperationsLimit)
	parsedFlags = append(parsedFlags, "\n\trate-limit-api", rateLimitRuleAPI)
	parsedFlags = append(parsedFlags, "\n\trate-limit-node", rateLimitRuleNode)

	// create current Node
	localNode, err = node.NewLocalNode(kp, bindEndpoint, "")
	if err != nil {
		cmdcommon.PrintError(nodeCmd, err)
	}
	localNode.AddValidators(validators...)
	localNode.SetPublishEndpoint(publishEndpoint)

	var vl []interface{}
	for _, v := range localNode.GetValidators() {
		vl = append(vl, "\n\tvalidator")
		vl = append(
			vl,
			fmt.Sprintf("alias=%s address=%s endpoint=%s", v.Alias(), v.Address(), v.Endpoint()),
		)
	}
	parsedFlags = append(parsedFlags, vl...)

	log.Debug("parsed flags:", parsedFlags...)

	if flagVerbose {
		http2.VerboseLogs = true
		network.VerboseLogs = true
	}
	if flagDebugPProf {
		runner.DebugPProf = true
	}
}

func getTime(timeoutStr string, defaultValue time.Duration, errMessage string) time.Duration {
	var timeoutDuration time.Duration
	if tmpUint64, err := strconv.ParseUint(timeoutStr, 10, 64); err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, errMessage, err)
	} else {
		timeoutDuration = time.Duration(tmpUint64) * time.Second
	}
	if timeoutDuration == 0 {
		timeoutDuration = defaultValue
	}
	return timeoutDuration
}

func getTimeDuration(str string, defaultValue time.Duration, errMessage string) time.Duration {
	if strings.TrimSpace(str) == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(str)
	if err != nil {
		cmdcommon.PrintFlagsError(nodeCmd, errMessage, err)
	}
	return d
}

func runNode() error {
	// create network
	networkConfig, err := network.NewHTTP2NetworkConfigFromEndpoint(localNode.Alias(), bindEndpoint)
	if err != nil {
		log.Crit("failed to create network", "error", err)
		return err
	}

	nt := network.NewHTTP2Network(networkConfig)

	policy, err := consensus.NewDefaultVotingThresholdPolicy(threshold)
	if err != nil {
		log.Crit("failed to create VotingThresholdPolicy", "error", err)
		return err
	}

	connectionManager := network.NewValidatorConnectionManager(
		localNode,
		nt,
		policy,
	)

	conf := common.Config{
		TimeoutINIT:       timeoutINIT,
		TimeoutSIGN:       timeoutSIGN,
		TimeoutACCEPT:     timeoutACCEPT,
		BlockTime:         blockTime,
		TxsLimit:          int(transactionsLimit),
		OpsLimit:          int(operationsLimit),
		RateLimitRuleAPI:  rateLimitRuleAPI,
		RateLimitRuleNode: rateLimitRuleNode,
	}
	st, err := storage.NewStorage(storageConfig)
	if err != nil {
		log.Crit("failed to initialize storage", "error", err)
		return err
	}

	c := sync.NewConfig([]byte(flagNetworkID), localNode, st, nt, connectionManager, conf)
	//Place setting config
	c.SyncPoolSize = syncPoolSize
	c.FetchTimeout = syncFetchTimeout
	c.RetryInterval = syncRetryInterval
	c.CheckBlockHeightInterval = syncCheckInterval

	syncer := c.NewSyncer()

	isaac, err := consensus.NewISAAC([]byte(flagNetworkID), localNode, policy, connectionManager, st, conf, syncer)
	if err != nil {
		log.Crit("failed to launch consensus", "error", err)
		return err
	}

	// Execution group.
	var g run.Group
	{
		nr, err := runner.NewNodeRunner(flagNetworkID, localNode, policy, nt, isaac, st, conf)

		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return err
		}

		g.Add(func() error {
			if err := nr.Start(); err != nil {
				log.Crit("failed to start node", "error", err)
				return err
			}
			return nil
		}, func(error) {
			nr.Stop()
		})
	}
	{
		g.Add(func() error {
			return syncer.Start()
		}, func(error) {
			syncer.Stop()
		})
	}
	{
		cancel := make(chan struct{})
		g.Add(func() error {
			return cmdcommon.Interrupt(cancel)
		}, func(error) {
			close(cancel)
		})
	}

	if err := g.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return err
	}

	return nil
}
