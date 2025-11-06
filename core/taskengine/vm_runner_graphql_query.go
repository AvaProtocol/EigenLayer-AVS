package taskengine

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/graphql"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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

		CommonProcessor: &CommonProcessor{vm: vm},
	}, nil
}

func (r *GraphqlQueryProcessor) Execute(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, any, error) {
	ctx := context.Background()

	// Use shared function to create execution step
	step := CreateNodeExecutionStep(stepID, r.GetTaskNode(), r.vm)

	// Add standardized log header
	r.sb.WriteString(formatNodeExecutionLogHeader(step))

	var err error
	defer func() {
		finalizeStep(step, err == nil, err, "", r.sb.String())
	}()

	// Get configuration from Config message (static configuration)
	if err = validateNodeConfig(node.Config, "GraphQLQueryNode"); err != nil {
		r.sb.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return step, nil, err
	}

	endpoint := node.Config.Url
	queryStr := node.Config.Query

	if endpoint == "" || queryStr == "" {
		err = NewMissingRequiredFieldError("url and query")
		return step, nil, err
	}

	// LANGUAGE ENFORCEMENT: GraphQLQueryNode uses GraphQL (hardcoded)
	// Using centralized ValidateInputByLanguage for consistency (includes size check)
	if err = ValidateInputByLanguage(queryStr, avsproto.Lang_LANG_GRAPHQL); err != nil {
		return step, nil, err
	}

	// Preprocess URL and query for template variables
	endpoint = r.vm.preprocessTextWithVariableMapping(endpoint)
	queryStr = r.vm.preprocessTextWithVariableMapping(queryStr)

	// Process GraphQL variables from Config
	variables := make(map[string]interface{})
	if node.Config.GetVariables() != nil {
		for key, value := range node.Config.GetVariables() {
			// Preprocess each variable value for template variables
			processedValue := r.vm.preprocessTextWithVariableMapping(value)
			variables[key] = processedValue
		}
	}

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
	r.sb.WriteString(fmt.Sprintf("Querying endpoint: %s\n", u.Hostname()))
	query := graphql.NewRequest(queryStr)

	// Add variables to the GraphQL request
	for key, value := range variables {
		query.Var(key, value)
	}

	// Add The Graph API key if querying gateway.thegraph.com
	if strings.Contains(u.Hostname(), "gateway.thegraph.com") {
		if apiKey, exists := r.vm.secrets["thegraph_api_key"]; exists && apiKey != "" {
			query.Header["Authorization"] = "Bearer " + apiKey
			r.sb.WriteString(fmt.Sprintf(" with API key authentication"))
		} else {
			r.sb.WriteString(fmt.Sprintf(" (no thegraph_api_key found in configuration)"))
		}
	}

	r.sb.WriteString(fmt.Sprintf("request variables: %v query: %v", variables, queryStr))
	err = client.Run(ctx, query, &resp)
	if err != nil {
		return step, nil, err
	}

	value, err := structpb.NewValue(resp)
	if err == nil {
		// Use the Value directly instead of wrapping in Any
		step.OutputData = &avsproto.Execution_Step_Graphql{
			Graphql: &avsproto.GraphQLQueryNode_Output{
				Data: value,
			},
		}
	}

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, resp)

	// Use shared function to finalize execution step with success
	finalizeStep(step, true, nil, "", r.sb.String())

	return step, resp, err
}
