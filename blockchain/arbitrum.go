package blockchain

import (
	"errors"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/smartcontractkit/external-initiator/store"
	"github.com/smartcontractkit/external-initiator/subscriber"
	"math/big"
)

const (
	ARB = "arbitrum"
)

// The arbManager implements the subscriber.JsonManager interface and allows
// for interacting with ETH nodes over RPC (not WS).
type arbManager struct {
	fq *filterQuery
	subType  subscriber.Type
}

func createArbManager(subType subscriber.Type, config store.Subscription) (arbManager, error) {
	if subType != subscriber.RPC {
		return arbManager{}, errors.New("only RPC connections are allowed for Arbitrum")
	}else{
		var addresses []common.Address
		for _, a := range config.Arbitrum.Addresses {
			addresses = append(addresses, common.HexToAddress(a))
		}

		var topics [][]common.Hash
		var t []common.Hash
		for _, value := range config.Arbitrum.Topics {
			if len(value) < 1 {
				continue
			}
			t = append(t, common.HexToHash(value))
		}
		topics = append(topics, t)

		return arbManager{
			fq: &filterQuery{
				Addresses: addresses,
				Topics:    topics,
			},
			subType: subType,
		}, nil
	}
}

// GetTriggerJson generates a JSON payload to the Arb node
// using the config in arbManager. Sends a "eth_getLogs" request.
func (e arbManager) GetTriggerJson() []byte {
	if e.subType == subscriber.RPC && e.fq.FromBlock == "" {
		e.fq.FromBlock = "latest"
	}

	filter, err := e.fq.toMapInterface()
	if err != nil {
		return nil
	}

	filterBytes, err := json.Marshal(filter)
	if err != nil {
		return nil
	}

	msg := JsonrpcMessage{
		Version: "2.0",
		ID:      json.RawMessage(`1`),
	}

	msg.Method = "eth_getLogs"
	msg.Params = json.RawMessage(`[` + string(filterBytes) + `]`)

	bytes, err := json.Marshal(msg)
	if err != nil {
		return nil
	}

	return bytes
}

type arbLogResponse struct {
	LogIndex         string   `json:"logIndex"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	Address          string   `json:"address"`
	Data             string   `json:"data"`
	Topics           []string `json:"topics"`
}

// ParseResponse parses the response from the
// ETH node, and returns a slice of subscriber.Events
// and if the parsing was successful.
//
// If arbManager is using RPC:
// If there are new events, update arbManager with
// the latest block number it sees.
func (e arbManager) ParseResponse(data []byte) ([]subscriber.Event, bool) {
	logger.Debugw("Parsing response", "ExpectsMock", ExpectsMock)

	var msg JsonrpcMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		logger.Error("failed parsing msg: ", msg)
		return nil, false
	}

	var events []subscriber.Event

	switch e.subType {
	case subscriber.RPC:
		var rawEvents []arbLogResponse
		if err := json.Unmarshal(msg.Result, &rawEvents); err != nil {
			return nil, false
		}

		for _, evt := range rawEvents {
			event, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			events = append(events, event)

			// Check if we can update the "fromBlock" in the query,
			// so we only get new events from blocks we haven't queried yet
			curBlkn, err := hexutil.DecodeBig(evt.BlockNumber)
			if err != nil {
				continue
			}
			// Increment the block number by 1, since we want events from *after* this block number
			curBlkn.Add(curBlkn, big.NewInt(1))

			fromBlkn, err := hexutil.DecodeBig(e.fq.FromBlock)
			if err != nil && !(e.fq.FromBlock == "latest" || e.fq.FromBlock == "") {
				continue
			}

			// If our query "fromBlock" is "latest", or our current "fromBlock" is in the past compared to
			// the last event we received, we want to update the query
			if e.fq.FromBlock == "latest" || e.fq.FromBlock == "" || curBlkn.Cmp(fromBlkn) > 0 {
				e.fq.FromBlock = hexutil.EncodeBig(curBlkn)
			}
		}
	}

	return events, true
}

// GetTestJson generates a JSON payload to test
// the connection to the ETH node.
//
//
// If arbManager is using RPC:
// Sends a request to get the latest block number.
func (e arbManager) GetTestJson() []byte {
	if e.subType == subscriber.RPC {
		msg := JsonrpcMessage{
			Version: "2.0",
			ID:      json.RawMessage(`1`),
			Method:  "eth_blockNumber",
		}

		bytes, err := json.Marshal(msg)
		if err != nil {
			return nil
		}

		return bytes
	}

	return nil
}

// ParseTestResponse parses the response from the
// ETH node after sending GetTestJson(), and returns
// the error from parsing, if any.
//
// Attempts to parse the block number in the response.
// If successful, stores the block number in arbManager.
func (e arbManager) ParseTestResponse(data []byte) error {
	if e.subType == subscriber.RPC {
		var msg JsonrpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		var res string
		if err := json.Unmarshal(msg.Result, &res); err != nil {
			return err
		}
		e.fq.FromBlock = res
	}

	return nil
}