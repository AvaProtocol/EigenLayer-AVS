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
}

func NewGraphqlQueryProcessor(endpoint string) *GraphqlQueryProcessor {
	client := graphql.NewClient(endpoint)

	return &GraphqlQueryProcessor{
		client: client,
	}
}

func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, error) {
	ctx := context.Background()
	t0 := time.Now().Unix()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		s.EndAt = time.Now().Unix()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	var resp map[string]any
	log.WriteString(fmt.Sprintf("Execute GraphQL %s at %s", node.Url, time.Now()))
	query := graphql.NewRequest(node.Query)
	err = r.client.Run(ctx, query, &resp)
	if err != nil {
		return s, err
	}

	s.Log = log.String()
	data, err := json.Marshal(resp)
	s.OutputData = string(data)
	return s, err
}
