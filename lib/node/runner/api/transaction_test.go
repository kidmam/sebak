package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"testing"

	"boscoin.io/sebak/lib/block"
	"boscoin.io/sebak/lib/common/observer"
	"boscoin.io/sebak/lib/node/runner/api/resource"
	"github.com/stretchr/testify/require"
)

func TestGetTransactionByHashHandler(t *testing.T) {

	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	_, _, bt, err := prepareTxWithoutSave(storage)
	require.NoError(t, err)
	bt.MustSave(storage)

	{ // unknown transaction
		req, _ := http.NewRequest("GET", ts.URL+GetTransactionsHandlerPattern+"/findme", nil)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	}

	var reader *bufio.Reader
	// Do a Request
	{
		respBody, err := request(ts, GetTransactionsHandlerPattern+"/"+bt.Hash, false)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}
	// Check the output
	{
		readByte, err := ioutil.ReadAll(reader)
		require.NoError(t, err)
		recv := make(map[string]interface{})
		json.Unmarshal(readByte, &recv)

		require.Equal(t, bt.Hash, recv["hash"], "hash is not same")
	}
}

func TestGetTransactionByHashHandlerStream(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	_, _, bt, err := prepareTxWithoutSave(storage)
	require.NoError(t, err)

	// Wait until request registered to observer
	{
		go func() {
			for {
				observer.BlockTransactionObserver.RLock()
				if len(observer.BlockTransactionObserver.Callbacks) > 0 {
					observer.BlockTransactionObserver.RUnlock()
					break
				}
				observer.BlockTransactionObserver.RUnlock()
			}
			err = bt.Save(storage)
			require.NoError(t, err)
			wg.Done()
		}()
	}

	// Do a Request
	var reader *bufio.Reader
	{
		respBody, err := request(ts, GetTransactionsHandlerPattern+"/"+bt.Hash, true)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}

	// Check the output
	{
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err)
		recv := make(map[string]interface{})
		json.Unmarshal(line, &recv)
		require.Equal(t, bt.Hash, recv["hash"], "hash is not same")
	}
	wg.Wait()
}

func TestGetTransactionsHandler(t *testing.T) {
	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	_, btList, err := prepareTxs(storage, 10)
	require.NoError(t, err)

	var reader *bufio.Reader
	{
		// Do a Request
		respBody, err := request(ts, GetTransactionsHandlerPattern, false)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}

	// Check the output
	{
		readByte, err := ioutil.ReadAll(reader)
		require.NoError(t, err)

		recv := make(map[string]interface{})
		json.Unmarshal(readByte, &recv)
		records := recv["_embedded"].(map[string]interface{})["records"].([]interface{})

		require.Equal(t, len(btList)+1, len(records), "length is not same")

		for i, r := range records[1:] {
			bt := r.(map[string]interface{})
			hash := bt["hash"].(string)

			require.Equal(t, hash, btList[i].Hash, "hash is not same")
		}
	}
}

func TestGetTransactionsHandlerStream(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	_, btList, err := prepareTxsWithoutSave(10, storage)
	require.NoError(t, err)
	btMap := make(map[string]block.BlockTransaction)
	for _, bt := range btList {
		btMap[bt.Hash] = bt
	}

	// Wait until request registered to observer
	{
		go func() {
			for {
				observer.BlockTransactionObserver.RLock()
				if len(observer.BlockTransactionObserver.Callbacks) > 0 {
					observer.BlockTransactionObserver.RUnlock()
					break
				}
				observer.BlockTransactionObserver.RUnlock()
			}
			for _, bt := range btMap {
				bt.MustSave(storage)
			}
			wg.Done()
		}()
	}

	// Do a Request
	var reader *bufio.Reader
	{
		respBody, err := request(ts, GetTransactionsHandlerPattern, true)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}

	// Check the output
	{
		// Discard the first entry (genesis)
		_, err := reader.ReadBytes('\n')
		require.NoError(t, err)
		for n := 0; n < 10; n++ {
			line, err := reader.ReadBytes('\n')
			require.NoError(t, err)
			line = bytes.Trim(line, "\n\t ")
			recv := make(map[string]interface{})
			json.Unmarshal(line, &recv)
			bt := btMap[recv["hash"].(string)]
			r := resource.NewTransaction(&bt)
			txS, err := json.Marshal(r.Resource())
			require.NoError(t, err)
			require.Equal(t, txS, line)
		}
	}
	wg.Wait()
}

func TestGetTransactionsByAccountHandler(t *testing.T) {
	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	kp, btList, err := prepareTxs(storage, 10)
	require.NoError(t, err)

	// Do a Request
	var reader *bufio.Reader
	{
		url := strings.Replace(GetAccountTransactionsHandlerPattern, "{id}", kp.Address(), -1)
		respBody, err := request(ts, url, false)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}

	// Check the output
	{
		readByte, err := ioutil.ReadAll(reader)
		require.NoError(t, err)

		recv := make(map[string]interface{})
		json.Unmarshal(readByte, &recv)
		records := recv["_embedded"].(map[string]interface{})["records"].([]interface{})

		require.Equal(t, len(btList), len(records), "length is not same")

		for i, r := range records {
			bt := r.(map[string]interface{})
			hash := bt["hash"].(string)

			require.Equal(t, hash, btList[i].Hash, "hash is not same")
		}
	}
}

func TestGetTransactionsByAccountHandlerStream(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	btMap := make(map[string]block.BlockTransaction)
	kp, btList, err := prepareTxsWithoutSave(10, storage)
	require.NoError(t, err)
	for _, bt := range btList {
		btMap[bt.Hash] = bt
	}

	// Wait until request registered to observer
	{
		go func() {
			for {
				observer.BlockTransactionObserver.RLock()
				if len(observer.BlockTransactionObserver.Callbacks) > 0 {
					observer.BlockTransactionObserver.RUnlock()
					break
				}
				observer.BlockTransactionObserver.RUnlock()
			}
			for _, bt := range btMap {
				bt.MustSave(storage)
			}
			wg.Done()
		}()
	}

	// Do a Request
	var reader *bufio.Reader
	{
		url := strings.Replace(GetAccountTransactionsHandlerPattern, "{id}", kp.Address(), -1)
		respBody, err := request(ts, url, true)
		require.NoError(t, err)
		defer respBody.Close()
		reader = bufio.NewReader(respBody)
	}

	// Check the output
	{
		for n := 0; n < 10; n++ {
			line, err := reader.ReadBytes('\n')
			require.NoError(t, err)
			line = bytes.Trim(line, "\n\t ")
			recv := make(map[string]interface{})
			json.Unmarshal(line, &recv)
			bt := btMap[recv["hash"].(string)]
			r := resource.NewTransaction(&bt)
			txS, err := json.Marshal(r.Resource())
			require.NoError(t, err)
			require.Equal(t, txS, line)
		}
	}
	wg.Wait()
}

func TestGetTransactionsHandlerPage(t *testing.T) {
	ts, storage, err := prepareAPIServer()
	require.NoError(t, err)
	defer storage.Close()
	defer ts.Close()

	_, btList, err := prepareTxs(storage, 10)
	require.NoError(t, err)

	requestFunction := func(url string) ([]interface{}, map[string]interface{}) {
		respBody, err := request(ts, url, false)
		require.NoError(t, err)
		defer respBody.Close()
		reader := bufio.NewReader(respBody)

		readByte, err := ioutil.ReadAll(reader)
		require.NoError(t, err)

		recv := make(map[string]interface{})
		json.Unmarshal(readByte, &recv)
		records := recv["_embedded"].(map[string]interface{})["records"].([]interface{})
		links := recv["_links"].(map[string]interface{})
		return records, links
	}

	testFunction := func(query string) ([]interface{}, map[string]interface{}) {
		return requestFunction(GetTransactionsHandlerPattern + "?" + query)
	}

	{
		query := strings.Replace(QueryPattern, "{cursor}", "", 1)
		query = strings.Replace(query, "{limit}", "0", 1)
		query = strings.Replace(query, "{reverse}", "false", 1)
		records, _ := testFunction(query)
		require.Equal(t, len(btList), len(records[1:]), "length is not same")

		for i, r := range records[1:] {
			bt := r.(map[string]interface{})
			require.Equal(t, bt["hash"], btList[i].Hash, "hash is not same")
		}
	}
	{
		query := strings.Replace(QueryPattern, "{cursor}", "", 1)
		query = strings.Replace(query, "{limit}", "6", 1)
		query = strings.Replace(query, "{reverse}", "false", 1)
		records, links := testFunction(query)
		require.Equal(t, len(btList[:5]), len(records[1:]), "length is not same")

		for i, r := range records[1:] {
			bt := r.(map[string]interface{})
			require.Equal(t, bt["hash"], btList[i].Hash, "hash is not same")
		}

		nextLink := links["next"].(map[string]interface{})["href"].(string)

		{
			records, _ := requestFunction(nextLink)
			require.Equal(t, len(btList[5:]), len(records), "length is not same")

			for i, r := range records {
				bt := r.(map[string]interface{})
				require.Equal(t, bt["hash"], btList[5+i].Hash, "hash is not same")
			}
		}
	}
	{
		query := strings.Replace(QueryPattern, "{cursor}", "", 1)
		query = strings.Replace(query, "{limit}", "0", 1)
		query = strings.Replace(query, "{reverse}", "true", 1)
		records, _ := testFunction(query)
		require.Equal(t, len(btList), len(records[:len(records)-1]), "length is not same")

		for i, r := range records[:len(records)-1] {
			bt := r.(map[string]interface{})
			require.Equal(t, bt["hash"], btList[len(btList)-1-i].Hash, "hash is not same")
		}
	}
	{
		query := strings.Replace(QueryPattern, "{cursor}", "", 1)
		query = strings.Replace(query, "{limit}", "5", 1)
		query = strings.Replace(query, "{reverse}", "true", 1)
		records, _ := testFunction(query)
		require.Equal(t, len(btList[5:]), len(records), "length is not same")

		for i, r := range records {
			bt := r.(map[string]interface{})
			require.Equal(t, bt["hash"], btList[len(btList)-1-i].Hash, "hash is not same")
		}
	}
}
