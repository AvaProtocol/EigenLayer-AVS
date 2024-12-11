// Go community is big on static type and codegen. As the result, many sophicated package are based on the idea of static typing the query and code generate the client

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/go-resty/resty/v2"
)

// Client is a client for interacting with a GraphQL API.
type Client struct {
	endpoint    string
	restyClient *resty.Client

	useMultipartForm bool

	// Log is called with various debug information.
	// To log to standard out, use:
	//  client.Log = func(s string) { log.Println(s) }
	Log func(s string)
}

// NewClient creates a new GraphQL client with the specified endpoint and options.
func NewClient(endpoint string, opts ...ClientOption) *Client {
	client := &Client{
		endpoint:    endpoint,
		restyClient: resty.New(),
		Log:         func(string) {},
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

func (c *Client) logf(format string, args ...interface{}) {
	c.Log(fmt.Sprintf(format, args...))
}

// Run executes the GraphQL query and unmarshals the response into the provided response object.
func (c *Client) Run(ctx context.Context, req *Request, resp interface{}) error {
	if c.useMultipartForm {
		return c.runWithMultipartForm(ctx, req, resp)
	}
	return c.runWithJSON(ctx, req, resp)
}

func (c *Client) runWithJSON(ctx context.Context, req *Request, resp interface{}) error {
	requestBody := map[string]interface{}{
		"query":     req.Query(),
		"variables": req.Vars(),
	}

	c.logf(">> variables: %v", req.Vars())
	c.logf(">> query: %s", req.Query())

	response, err := c.restyClient.R().
		SetContext(ctx).
		SetHeaders(req.Header).
		SetBody(requestBody).
		Post(c.endpoint)

	if err != nil {
		return err
	}

	c.logf("<< status: %d", response.StatusCode())
	c.logf("<< body: %s", response.String())

	if response.IsError() {
		return fmt.Errorf("graphql: server returned a non-200 status code: %d", response.StatusCode())
	}

	return c.parseResponse(response.Body(), resp)
}

func (c *Client) runWithMultipartForm(ctx context.Context, req *Request, resp interface{}) error {
	request := c.restyClient.R().SetContext(ctx)

	request.SetFormData(map[string]string{
		"query": req.Query(),
	})

	if len(req.Vars()) > 0 {
		variablesJSON, err := json.Marshal(req.Vars())
		if err != nil {
			return fmt.Errorf("error encoding variables: %w", err)
		}
		request.SetFormData(map[string]string{
			"variables": string(variablesJSON),
		})
	}

	for _, file := range req.Files() {
		request.SetFileReader(file.Field, file.Name, file.R)
	}

	response, err := request.Post(c.endpoint)
	if err != nil {
		return err
	}

	c.logf("<< status: %d", response.StatusCode())
	c.logf("<< body: %s", response.String())

	if response.IsError() {
		return fmt.Errorf("graphql: server returned a non-200 status code: %d", response.StatusCode())
	}

	return c.parseResponse(response.Body(), resp)
}

func (c *Client) parseResponse(body []byte, resp interface{}) error {
	gr := &graphResponse{Data: resp}
	if err := json.Unmarshal(body, gr); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	if len(gr.Errors) > 0 {
		return gr.Errors[0]
	}
	return nil
}

// ClientOption defines a configuration option for the Client.
type ClientOption func(*Client)

// WithRestyClient sets a custom Resty client.
func WithRestyClient(client *resty.Client) ClientOption {
	return func(c *Client) {
		c.restyClient = client
	}
}

// UseMultipartForm enables multipart/form-data support.
func UseMultipartForm() ClientOption {
	return func(c *Client) {
		c.useMultipartForm = true
	}
}

// Request represents a GraphQL request.
type Request struct {
	query  string
	vars   map[string]interface{}
	files  []File
	Header map[string]string
}

// NewRequest creates a new GraphQL request.
func NewRequest(query string) *Request {
	return &Request{
		query:  query,
		vars:   make(map[string]interface{}),
		Header: make(map[string]string),
	}
}

// Var sets a variable for the GraphQL request.
func (r *Request) Var(key string, value interface{}) {
	r.vars[key] = value
}

// Vars returns the variables of the request.
func (r *Request) Vars() map[string]interface{} {
	return r.vars
}

// Query returns the GraphQL query string.
func (r *Request) Query() string {
	return r.query
}

// File sets a file to upload.
func (r *Request) File(fieldname, filename string, reader io.Reader) {
	r.files = append(r.files, File{
		Field: fieldname,
		Name:  filename,
		R:     reader,
	})
}

// Files returns the files associated with the request.
func (r *Request) Files() []File {
	return r.files
}

// File represents a file to upload.
type File struct {
	Field string
	Name  string
	R     io.Reader
}

type graphErr struct {
	Message string
}

func (e graphErr) Error() string {
	return "graphql: " + e.Message
}

type graphResponse struct {
	Data   interface{}
	Errors []graphErr
}
