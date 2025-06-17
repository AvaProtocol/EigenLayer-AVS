package taskengine

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/graphql"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type GraphqlQueryProcessor struct {
	*CommonProcessor

	client *graphql.Client
	sb     *strings.Builder
	url    *url.URL
}

func NewGraphqlQueryProcessor(vm *VM) (*GraphqlQueryProcessor, error) {
	sb := &strings.Builder{}

	return &GraphqlQueryProcessor{
		client: nil, // Will be initialized when we have the URL from Config
		sb:     sb,
		url:    nil, // Will be set when we have the URL from Config

		CommonProcessor: &CommonProcessor{vm},
	}, nil
}

func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, any, error) {
	ctx := context.Background()
	t0 := time.Now().UnixMilli()

	// Get node data using helper function to reduce duplication
	nodeName, nodeInput := r.vm.GetNodeDataForExecution(stepID)

	step := &avsproto.Execution_Step{
		Id:         stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
		Type:       avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY.String(),
		Name:       nodeName,
		Input:      nodeInput, // Include node input data for debugging
	}

	var err error
	defer func() {
		step.EndAt = time.Now().UnixMilli()
		step.Success = err == nil
		if err != nil {
			step.Error = err.Error()
		}
	}()

	// Get configuration from Config message (static configuration)
	if node.Config == nil {
		err = fmt.Errorf("GraphQLQueryNode Config is nil")
		return step, nil, err
	}

	endpoint := node.Config.Url
	queryStr := node.Config.Query

	if endpoint == "" || queryStr == "" {
		err = fmt.Errorf("missing required configuration: url and query")
		return step, nil, err
	}

	// Preprocess URL and query for template variables
	endpoint = r.vm.preprocessTextWithVariableMapping(endpoint)
	queryStr = r.vm.preprocessTextWithVariableMapping(queryStr)

	// Initialize client with the URL from Config
	log := func(s string) {
		r.sb.WriteString(s)
	}

	client, err := graphql.NewClient(endpoint, log)
	if err != nil {
		return step, nil, err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return step, nil, err
	}

	var resp map[string]any
	r.sb.WriteString(fmt.Sprintf("Execute GraphQL %s at %s", u.Hostname(), time.Now()))
	query := graphql.NewRequest(queryStr)
	err = client.Run(ctx, query, &resp)
	if err != nil {
		return step, nil, err
	}

	step.Log = r.sb.String()

	value, err := structpb.NewValue(resp)
	if err == nil {
		pbResult, _ := anypb.New(value)
		step.OutputData = &avsproto.Execution_Step_Graphql{
			Graphql: &avsproto.GraphQLQueryNode_Output{
				Data: pbResult,
			},
		}

	}

	r.SetOutputVarForStep(stepID, resp)
	return step, resp, err
}
