package metrics

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients/elcontracts"
	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/Layr-Labs/eigensdk-go/metrics/collectors/economic"
	"github.com/Layr-Labs/eigensdk-go/types"
)

func NewMetricsOnlyEconomicCollector(
	elReader elcontracts.ELReader,
	avsReader interface{},
	avsName string,
	logger logging.Logger,
	operatorAddr common.Address,
	quorumNames map[types.QuorumNum]string,
) prometheus.Collector {
	return economic.NewMetricsOnlyCollector(
		elReader,
		avsReader,
		avsName,
		logger,
		operatorAddr,
		quorumNames,
	)
}
