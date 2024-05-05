package operator

import (
	"net/rpc"

	//"github.com/OAK-Foundation/oak-avs/aggregator"

	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/OAK-Foundation/oak-avs/metrics"
)

type AggregatorRpcClienter interface {
	//SendSignedTaskResponseToAggregator(signedTaskResponse *aggregator.SignedTaskResponse)
}
type AggregatorRpcClient struct {
	rpcClient            *rpc.Client
	metrics              metrics.Metrics
	logger               logging.Logger
	aggregatorIpPortAddr string
}

func NewAggregatorRpcClient(aggregatorIpPortAddr string, logger logging.Logger, metrics metrics.Metrics) (*AggregatorRpcClient, error) {
	return &AggregatorRpcClient{
		// set to nil so that we can create an rpc client even if the aggregator is not running
		rpcClient:            nil,
		metrics:              metrics,
		logger:               logger,
		aggregatorIpPortAddr: aggregatorIpPortAddr,
	}, nil
}

func (c *AggregatorRpcClient) dialAggregatorRpcClient() error {
	client, err := rpc.DialHTTP("tcp", c.aggregatorIpPortAddr)
	if err != nil {
		return err
	}
	c.rpcClient = client
	return nil
}

// SendSignedTaskResponseToAggregator sends a signed task response to the aggregator.
// it is meant to be ran inside a go thread, so doesn't return anything.
// this is because sending the signed task response to the aggregator is time sensitive,
// so there is no point in retrying if it fails for a few times.
// Currently hardcoded to retry sending the signed task response 5 times, waiting 2 seconds in between each attempt.
// func (c *AggregatorRpcClient) SendSignedTaskResponseToAggregator(signedTaskResponse *aggregator.SignedTaskResponse) {
// 	return nil
// }
