package api

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gorilla/mux"
	"github.com/stellar/go/keypair"

	"boscoin.io/sebak/lib/block"
	"boscoin.io/sebak/lib/storage"
	"boscoin.io/sebak/lib/transaction"
)

var networkID []byte = []byte("sebak-test-network")

const (
	QueryPattern = "cursor={cursor}&limit={limit}&reverse={reverse}&type={type}"
)

func prepareAPIServer() (*httptest.Server, *storage.LevelDBBackend, error) {
	storage := block.InitTestBlockchain()
	apiHandler := NetworkHandlerAPI{storage: storage}

	router := mux.NewRouter()
	router.HandleFunc(GetAccountHandlerPattern, apiHandler.GetAccountHandler).Methods("GET")
	router.HandleFunc(GetAccountTransactionsHandlerPattern, apiHandler.GetTransactionsByAccountHandler).Methods("GET")
	router.HandleFunc(GetAccountOperationsHandlerPattern, apiHandler.GetOperationsByAccountHandler).Methods("GET")
	router.HandleFunc(GetTransactionsHandlerPattern, apiHandler.GetTransactionsHandler).Methods("GET")
	router.HandleFunc(GetTransactionByHashHandlerPattern, apiHandler.GetTransactionByHashHandler).Methods("GET")
	router.HandleFunc(GetAccountHandlerPattern, apiHandler.GetAccountHandler).Methods("GET")
	router.HandleFunc(GetAccountHandlerPattern, apiHandler.GetAccountHandler).Methods("GET")
	router.HandleFunc(GetAccountHandlerPattern, apiHandler.GetAccountHandler).Methods("GET")
	router.HandleFunc(GetTransactionOperationsHandlerPattern, apiHandler.GetOperationsByTxHashHandler).Methods("GET")
	ts := httptest.NewServer(router)
	return ts, storage, nil
}

func prepareOps(storage *storage.LevelDBBackend, count int) (*keypair.Full, []block.BlockOperation, error) {
	kp, btList, err := prepareTxs(storage, count)
	if err != nil {
		return nil, nil, err
	}
	var boList []block.BlockOperation
	for _, bt := range btList {
		bo, err := block.GetBlockOperation(storage, bt.Operations[0])
		if err != nil {
			return nil, nil, err
		}
		boList = append(boList, bo)
	}

	return kp, boList, nil
}
func prepareOpsWithoutSave(count int, st *storage.LevelDBBackend) (*keypair.Full, []block.BlockOperation, error) {

	kp, err := keypair.Random()
	if err != nil {
		return nil, nil, err
	}
	var txs []transaction.Transaction
	var txHashes []string
	var boList []block.BlockOperation
	for i := 0; i < count; i++ {
		tx := transaction.TestMakeTransactionWithKeypair(networkID, 1, kp)
		txs = append(txs, tx)
		txHashes = append(txHashes, tx.GetHash())
	}

	theBlock := block.TestMakeNewBlockWithPrevBlock(block.GetLatestBlock(st), txHashes)
	for _, tx := range txs {
		for _, op := range tx.B.Operations {
			bo, err := block.NewBlockOperationFromOperation(op, tx, theBlock.Height)
			if err != nil {
				panic(err)
			}
			boList = append(boList, bo)
		}
	}

	return kp, boList, nil
}

func prepareTxs(storage *storage.LevelDBBackend, count int) (*keypair.Full, []block.BlockTransaction, error) {
	kp, err := keypair.Random()
	if err != nil {
		return nil, nil, err
	}
	var txs []transaction.Transaction
	var txHashes []string
	var btList []block.BlockTransaction
	for i := 0; i < count; i++ {
		tx := transaction.TestMakeTransactionWithKeypair(networkID, 1, kp)
		txs = append(txs, tx)
		txHashes = append(txHashes, tx.GetHash())
	}

	theBlock := block.TestMakeNewBlockWithPrevBlock(block.GetLatestBlock(storage), txHashes)
	err = theBlock.Save(storage)
	if err != nil {
		return nil, nil, err
	}
	for _, tx := range txs {
		bt := block.NewBlockTransactionFromTransaction(theBlock.Hash, theBlock.Height, theBlock.Confirmed, tx)
		err = bt.Save(storage)
		if err != nil {
			return nil, nil, err
		}
		btList = append(btList, bt)
	}
	return kp, btList, nil
}

func prepareTxsWithoutSave(count int, st *storage.LevelDBBackend) (*keypair.Full, []block.BlockTransaction, error) {
	kp, err := keypair.Random()
	if err != nil {
		return nil, nil, err
	}
	var txs []transaction.Transaction
	var txHashes []string
	var btList []block.BlockTransaction
	for i := 0; i < count; i++ {
		tx := transaction.TestMakeTransactionWithKeypair(networkID, 1, kp)
		txs = append(txs, tx)
		txHashes = append(txHashes, tx.GetHash())
	}

	theBlock := block.TestMakeNewBlockWithPrevBlock(block.GetLatestBlock(st), txHashes)
	for _, tx := range txs {
		bt := block.NewBlockTransactionFromTransaction(theBlock.Hash, theBlock.Height, theBlock.Confirmed, tx)
		btList = append(btList, bt)
	}
	return kp, btList, nil
}

func prepareTxWithoutSave(st *storage.LevelDBBackend) (*keypair.Full, *transaction.Transaction, *block.BlockTransaction, error) {
	kp, err := keypair.Random()
	if err != nil {
		return nil, nil, nil, err
	}

	tx := transaction.TestMakeTransactionWithKeypair(networkID, 1, kp)

	theBlock := block.TestMakeNewBlockWithPrevBlock(block.GetLatestBlock(st), []string{tx.GetHash()})
	bt := block.NewBlockTransactionFromTransaction(theBlock.Hash, theBlock.Height, theBlock.Confirmed, tx)
	return kp, &tx, &bt, nil
}

func request(ts *httptest.Server, url string, streaming bool) (io.ReadCloser, error) {
	// Do a Request
	url = ts.URL + url
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
