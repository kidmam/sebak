package runner

import (
	"boscoin.io/sebak/lib/ballot"
	"boscoin.io/sebak/lib/block"
	"boscoin.io/sebak/lib/common"
	"boscoin.io/sebak/lib/errors"
	"boscoin.io/sebak/lib/node"
	"boscoin.io/sebak/lib/storage"
	"boscoin.io/sebak/lib/transaction"
	"boscoin.io/sebak/lib/transaction/operation"
	"boscoin.io/sebak/lib/voting"
)

type BallotTransactionChecker struct {
	common.DefaultChecker

	NodeRunner *NodeRunner
	LocalNode  node.Node
	NetworkID  []byte

	Ballot                ballot.Ballot
	Transactions          []string
	VotingHole            voting.Hole
	ValidTransactions     []string
	validTransactionsMap  map[string]bool
	CheckTransactionsOnly bool
}

func (checker *BallotTransactionChecker) InvalidTransactions() (invalids []string) {
	for _, hash := range checker.Transactions {
		if _, found := checker.validTransactionsMap[hash]; found {
			continue
		}

		invalids = append(invalids, hash)
	}

	return
}

func (checker *BallotTransactionChecker) setValidTransactions(hashes []string) {
	checker.ValidTransactions = hashes

	checker.validTransactionsMap = map[string]bool{}
	for _, hash := range hashes {
		checker.validTransactionsMap[hash] = true
	}

	return
}

// TransactionsIsNew checks the incoming transaction is
// already stored or not.
func IsNew(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	var validTransactions []string
	for _, hash := range checker.Transactions {
		// check transaction is already stored
		var found bool
		if found, err = block.ExistsBlockTransaction(checker.NodeRunner.Storage(), hash); err != nil || found {
			if !checker.CheckTransactionsOnly {
				err = errors.NewButKnownMessage
				return
			}
			continue
		}
		validTransactions = append(validTransactions, hash)
	}

	err = nil
	checker.setValidTransactions(validTransactions)

	return
}

// CheckMissingTransaction will get the missing tranactions, that is, not in
// `Pool` from proposer.
func CheckMissingTransaction(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	var validTransactions []string
	for _, hash := range checker.ValidTransactions {
		if !checker.NodeRunner.TransactionPool.Has(hash) {
			continue
		}
		validTransactions = append(validTransactions, hash)
	}

	checker.setValidTransactions(validTransactions)

	return
}

// BallotTransactionsSameSource checks there are transactions which has same
// source in the `Transactions`.
func BallotTransactionsSameSource(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	var validTransactions []string
	sources := map[string]bool{}
	for _, hash := range checker.ValidTransactions {
		tx, _ := checker.NodeRunner.TransactionPool.Get(hash)
		if found := common.InStringMap(sources, tx.B.Source); found {
			if !checker.CheckTransactionsOnly {
				err = errors.TransactionSameSource
				return
			}
			continue
		}

		sources[tx.B.Source] = true
		validTransactions = append(validTransactions, hash)
	}
	err = nil
	checker.setValidTransactions(validTransactions)

	return
}

// BallotTransactionsSourceCheck calls `Transaction.Validate()`.
func BallotTransactionsSourceCheck(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	var validTransactions []string
	for _, hash := range checker.ValidTransactions {
		tx, _ := checker.NodeRunner.TransactionPool.Get(hash)

		if err = ValidateTx(checker.NodeRunner.Storage(), tx); err != nil {
			if !checker.CheckTransactionsOnly {
				return
			}
			continue
		}
		validTransactions = append(validTransactions, hash)
	}

	err = nil
	checker.setValidTransactions(validTransactions)

	return
}

// BallotTransactionsOperationBodyCollectTxFee validates the
// `BallotTransactionsOperationBodyCollectTxFee.Amount` is matched with the
// collected fee of all transactions.
func BallotTransactionsOperationBodyCollectTxFee(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	var opb operation.CollectTxFee
	if opb, err = checker.Ballot.ProposerTransaction().CollectTxFee(); err != nil {
		return
	}

	// check the colleted transaction fee is matched with
	// `CollectTxFee.Amount`
	if checker.Ballot.TransactionsLength() < 1 {
		if opb.Amount != 0 {
			err = errors.InvalidOperation
			return
		}
	} else {
		var fee common.Amount
		for _, hash := range checker.Transactions {
			if tx, found := checker.NodeRunner.TransactionPool.Get(hash); !found {
				err = errors.TransactionNotFound
				return
			} else {
				fee = fee.MustAdd(tx.B.Fee)
			}
		}
		if opb.Amount != fee {
			err = errors.InvalidFee
			return
		}
	}

	return
}

// BallotTransactionsAllValid checks all the transactions are valid or not.
func BallotTransactionsAllValid(c common.Checker, args ...interface{}) (err error) {
	checker := c.(*BallotTransactionChecker)

	if len(checker.InvalidTransactions()) > 0 {
		checker.VotingHole = voting.NO
	} else {
		checker.VotingHole = voting.YES
	}

	return
}

//
// Validate the entirety of a transaction
//
// This function is critical for consensus, as it defines the validation rules for a transaction.
// It is executed whenever a transaction is received.
//
// As it is run on every single transaction, it should be as fast as possible,
// thus any new code should carefully consider when it takes place.
// Consider putting cheap, frequent errors first.
//
// Note that if we get to this function, it means the transaction is known to be
// well-formed (e.g. the signature is legit).
//
// Params:
//   st = Storage backend to use (e.g. to access the blocks)
//        Only ever read from, never written to.
//   tx = Transaction to check
//
func ValidateTx(st *storage.LevelDBBackend, tx transaction.Transaction) (err error) {
	// check, source exists
	var ba *block.BlockAccount
	if ba, err = block.GetBlockAccount(st, tx.B.Source); err != nil {
		err = errors.BlockAccountDoesNotExists
		return
	}

	// check, sequenceID is based on latest sequenceID
	if !tx.IsValidSequenceID(ba.SequenceID) {
		err = errors.TransactionInvalidSequenceID
		return
	}

	// get the balance at sequenceID
	var bac block.BlockAccountSequenceID
	bac, err = block.GetBlockAccountSequenceID(st, tx.B.Source, tx.B.SequenceID)
	if err != nil {
		return
	}

	totalAmount := tx.TotalAmount(true)

	// check, have enough balance at sequenceID
	if bac.Balance < totalAmount {
		err = errors.TransactionExcessAbilityToPay
		return
	}

	for _, op := range tx.B.Operations {
		if err = ValidateOp(st, ba, op); err != nil {

			key := block.GetBlockTransactionHistoryKey(tx.H.Hash)
			st.Remove(key)

			return
		}
	}

	return
}

//
// Validate an operation
//
// This function is critical for consensus, as it defines the validation rules for an operation,
// and by extension a transaction. It is called from ValidateTx.
//
// Params:
//   st = Storage backend to use (e.g. to access the blocks)
//        Only ever read from, never written to.
//   source = Account from where the transaction (and ops) come from
//   tx = Transaction to check
//
func ValidateOp(st *storage.LevelDBBackend, source *block.BlockAccount, op operation.Operation) (err error) {
	switch op.H.Type {
	case operation.TypeCreateAccount:
		var ok bool
		var casted operation.CreateAccount
		if casted, ok = op.B.(operation.CreateAccount); !ok {
			return errors.TypeOperationBodyNotMatched
		}
		var exists bool
		if exists, err = block.ExistsBlockAccount(st, op.B.(operation.CreateAccount).Target); err == nil && exists {
			return errors.BlockAccountAlreadyExists
		}
		if source.Linked != "" {
			// Unfreezing must be done after X period from unfreezing request
			iterFunc, closeFunc := block.GetBlockOperationsBySource(st, source.Address, nil)
			bo, _, _ := iterFunc()
			closeFunc()
			// Before unfreezing payment, unfreezing request shoud be saved
			if bo.Type != operation.TypeUnfreezingRequest {
				return errors.UnfreezingRequestNotRequested
			}
			lastblock := block.GetLatestBlock(st)
			// unfreezing period is 241920.
			if lastblock.Height-bo.Height < common.UnfreezingPeriod {
				return errors.UnfreezingNotReachedExpiration
			}
			// If it's a frozen account we check that only whole units are frozen
			if casted.Linked != "" && (casted.Amount%common.Unit) != 0 {
				return errors.FrozenAccountCreationWholeUnit // FIXME
			}
		}
	case operation.TypePayment:
		var ok bool
		var casted operation.Payment
		if casted, ok = op.B.(operation.Payment); !ok {
			return errors.TypeOperationBodyNotMatched
		}
		var taccount *block.BlockAccount
		if taccount, err = block.GetBlockAccount(st, casted.Target); err != nil {
			return errors.BlockAccountDoesNotExists
		}
		// If it's a frozen account, it cannot receive payment
		if taccount.Linked != "" {
			return errors.FrozenAccountNoDeposit
		}
		if source.Linked != "" {
			// If it's a frozen account, everything must be withdrawn
			var expected common.Amount
			expected, err = source.Balance.Sub(common.BaseFee)
			if casted.Amount != expected {
				return errors.FrozenAccountMustWithdrawEverything
			}
			// Unfreezing must be done after X period from unfreezing request
			iterFunc, closeFunc := block.GetBlockOperationsBySource(st, source.Address, nil)
			bo, _, _ := iterFunc()
			closeFunc()
			// Before unfreezing payment, unfreezing request shoud be saved
			if bo.Type != operation.TypeUnfreezingRequest {
				return errors.UnfreezingRequestNotRequested
			}
			lastblock := block.GetLatestBlock(st)
			// unfreezing period is 241920.
			if lastblock.Height-bo.Height < common.UnfreezingPeriod {
				return errors.UnfreezingNotReachedExpiration
			}
		}
	case operation.TypeUnfreezingRequest:
		if _, ok := op.B.(operation.UnfreezeRequest); !ok {
			return errors.TypeOperationBodyNotMatched
		}
		// Unfreezing should be done from a frozen account
		if source.Linked == "" {
			return errors.UnfreezingFromInvalidAccount
		}
		// Repeated unfreeze request shoud be blocked after unfreeze request saved
		iterFunc, closeFunc := block.GetBlockOperationsBySource(st, source.Address, nil)
		bo, _, _ := iterFunc()
		closeFunc()
		if bo.Type == operation.TypeUnfreezingRequest {
			return errors.UnfreezingRequestAlreadyReceived
		}
	case operation.TypeCongressVoting, operation.TypeCongressVotingResult:
		// Nothing to do
		return

	default:
		return errors.UnknownOperationType
	}
	return
}
