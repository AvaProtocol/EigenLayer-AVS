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

	// Use shared function to create execution step
	step := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY, r.vm)

	var err error
	defer func() {
		if err != nil {
			finalizeExecutionStep(step, false, err.Error(), r.sb.String())
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

	value, err := structpb.NewValue(resp)
	if err == nil {
		pbResult, _ := anypb.New(value)
		step.OutputData = &avsproto.Execution_Step_Graphql{
			Graphql: &avsproto.GraphQLQueryNode_Output{
				Data: pbResult,
			},
		}
	}

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, resp)

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(step, true, "", r.sb.String())

	return step, resp, err
}
