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

func NewGraphqlQueryProcessor(vm *VM, endpoint string) (*GraphqlQueryProcessor, error) {
	sb := &strings.Builder{}
	log := func(s string) {
		sb.WriteString(s)
	}

	client, err := graphql.NewClient(endpoint, log)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	return &GraphqlQueryProcessor{
		client: client,
		sb:     sb,
		url:    u,

		CommonProcessor: &CommonProcessor{vm},
	}, nil
}

func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, any, error) {
	ctx := context.Background()
	t0 := time.Now().UnixMilli()
	step := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		step.EndAt = time.Now().UnixMilli()
		step.Success = err == nil
		if err != nil {
			step.Error = err.Error()
		}
	}()

	var resp map[string]any
	r.sb.WriteString(fmt.Sprintf("Execute GraphQL %s at %s", r.url.Hostname(), time.Now()))
	query := graphql.NewRequest(node.Query)
	err = r.client.Run(ctx, query, &resp)
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
