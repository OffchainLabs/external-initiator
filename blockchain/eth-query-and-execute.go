package blockchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/status-im/keycard-go/hexutils"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/smartcontractkit/chainlink/core/logger"
	"github.com/smartcontractkit/external-initiator/store"
	"github.com/smartcontractkit/external-initiator/subscriber"
)

const (
	ETH_QAE            = "eth-query-and-execute"
	ethTrue            = "0x0000000000000000000000000000000000000000000000000000000000000001"
	defaultResponseKey = "value"
)

func createEthQaeSubscriber(sub store.Subscription) (ethQaeSubscriber, error) {
	contractAbi, err := abi.JSON(bytes.NewReader(sub.EthQae.ABI))
	if err != nil {
		return ethQaeSubscriber{}, err
	}

	return ethQaeSubscriber{
		Endpoint:    strings.TrimSuffix(sub.Endpoint.Url, "/"),
		Address:     common.HexToAddress(sub.EthQae.Address),
		ABI:         contractAbi,
		Job:         sub.Job,
		ResponseKey: sub.EthQae.ResponseKey,
		MethodName:  sub.EthQae.MethodName,
	}, nil
}

type ethQaeSubscriber struct {
	Endpoint    string
	Address     common.Address
	ABI         abi.ABI
	MethodName  string
	Job         string
	ResponseKey string
}

type ethQaeSubscription struct {
	endpoint string
	events   chan<- subscriber.Event
	address  common.Address
	abi      abi.ABI
	method   string
	isDone   bool
	jobid    string
	key      string
}

func (ethQae ethQaeSubscriber) SubscribeToEvents(channel chan<- subscriber.Event, _ ...interface{}) (subscriber.ISubscription, error) {
	logger.Infof("Using Ethereum RPC endpoint: %s", ethQae.Endpoint)
	logger.Infof("Using function selector %s on contract %s", ethQae.MethodName, ethQae.Address.String())

	sub := ethQaeSubscription{
		endpoint: ethQae.Endpoint,
		events:   channel,
		jobid:    ethQae.Job,
		address:  ethQae.Address,
		abi:      ethQae.ABI,
		method:   ethQae.MethodName,
		key:      ethQae.ResponseKey,
	}

	go sub.readMessagesWithRetry()

	return sub, nil
}

func (ethQae ethQaeSubscriber) Test() error {
	msg := jsonrpcMessage{
		Version: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "eth_blockNumber",
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	resp, err := sendEthQaePost(ethQae.Endpoint, payload)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (ethQae ethQaeSubscription) readMessagesWithRetry() {
	for {
		if ethQae.isDone {
			return
		}
		ethQae.readMessages()
		time.Sleep(monitorRetryInterval)
	}
}

type ethCall struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to"`
	Gas      string `json:"gas,omitempty"`
	GasPrice string `json:"gasPrice,omitempty"`
	Value    string `json:"value,omitempty"`
	Data     string `json:"data,omitempty"`
}

func (ethQae ethQaeSubscription) getPayload() (*jsonrpcMessage, error) {
	data, err := ethQae.abi.Pack(ethQae.method)
	if err != nil {
		return nil, err
	}

	call := ethCall{
		To:   ethQae.address.Hex(),
		Data: hexutil.Encode(data[:]),
	}

	callBz, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}

	return &jsonrpcMessage{
		Version: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "eth_call",
		Params:  json.RawMessage(`[` + string(callBz) + `, "latest"]`),
	}, nil
}

func (ethQae ethQaeSubscription) readMessages() {
	msg, err := ethQae.getPayload()
	if err != nil {
		logger.Error("Unable to get ETH QAE payload:", err)
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		logger.Error("Unable to marshal ETH QAE payload:", err)
		return
	}

	logger.Debug(string(payload))

	resp, err := sendEthQaePost(ethQae.endpoint, payload)
	if err != nil {
		logger.Error(err)
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Error(err)
		return
	}

	var response jsonrpcMessage
	err = json.Unmarshal(body, &response)
	if err != nil {
		logger.Error(err)
		return
	}

	var result string
	err = json.Unmarshal(response.Result, &result)
	if err != nil {
		logger.Error(err)
		return
	}

	// Remove 0x prefix
	if strings.HasPrefix(result, "0x") {
		result = result[2:]
	}

	resultData := hexutils.HexToBytes(result)

	b, err := unpackResultIntoBool(ethQae.abi, ethQae.method, resultData)
	if err == nil {
		if *b == true {
			ethQae.events <- subscriber.Event{}
		}
		return
	}

	res, err := unpackResultIntoAddresses(ethQae.abi, ethQae.method, resultData)
	if err != nil {
		logger.Error(err)
		return
	}

	for _, r := range *res {
		logger.Debug("Got address: ", r.String())

		event := map[string]interface{}{
			ethQae.key: r,
		}
		bz, err := json.Marshal(event)
		if err != nil {
			logger.Error(err)
			continue
		}
		ethQae.events <- bz
	}
}

func sendEthQaePost(endpoint string, payload []byte) (*http.Response, error) {
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned 400. This endpoint may not support calls to /monitor", endpoint)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("Unexpected status code %v from endpoint %s", resp.StatusCode, endpoint)
	}
	return resp, nil
}

func (ethQae ethQaeSubscription) Unsubscribe() {
	logger.Info("Unsubscribing from ETH QAE endpoint", ethQae.endpoint)
	ethQae.isDone = true
}
