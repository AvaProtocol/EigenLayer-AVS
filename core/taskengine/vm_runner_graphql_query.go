package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AvaProtocol/ap-avs/pkg/graphql"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type GraphqlQueryProcessor struct {
	client *graphql.Client
	sb     *strings.Builder
}

func NewGraphqlQueryProcessor(endpoint string) (*GraphqlQueryProcessor, error) {
	sb := &strings.Builder{}
	log := func(s string) {
		sb.WriteString(s)
	}

	client, err := graphql.NewClient(endpoint, log)
	if err != nil {
		return nil, err
	}

	return &GraphqlQueryProcessor{
		client: client,
		sb:     sb,
	}, nil
}

func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, error) {
	ctx := context.Background()
	t0 := time.Now().Unix()
	step := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		step.EndAt = time.Now().Unix()
		step.Success = err == nil
		if err != nil {
			step.Error = err.Error()
		}
	}()

	var resp map[string]any
	r.sb.WriteString(fmt.Sprintf("Execute GraphQL %s at %s", node.Url, time.Now()))
	query := graphql.NewRequest(node.Query)
	err = r.client.Run(ctx, query, &resp)
	if err != nil {
		return step, err
	}

	step.Log = r.sb.String()
	data, err := json.Marshal(resp)
	step.OutputData = string(data)
	return step, err
}
