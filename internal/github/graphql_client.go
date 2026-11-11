package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	gogithub "github.com/google/go-github/v90/github"
	"github.com/shurcooL/githubv4"
)

// githubv4 discards HTTP headers and GraphQL error types/paths. Retain them
// per request for token failover and alias-scoped retries, including partial
// data whose absent and null aliases must remain distinguishable.
type gqlResponseKey struct{}
type gqlWireResponse struct {
	Data   json.RawMessage
	Errors []gqlErrorItem
}

type gqlErrorItem struct {
	Message string
	Type    string
	Path    []interface{}
}

type gqlResponseError struct{ Errors []gqlErrorItem }

func (e *gqlResponseError) Error() string {
	return fmt.Sprintf("GraphQL: %s", e.Errors[0].Message)
}

type gqlTransport struct {
	token string
	base  http.RoundTripper
}

const maxGraphQLResponseBytes = 32 << 20

func (t *gqlTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("User-Agent", restUserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphQLResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxGraphQLResponseBytes {
		return nil, fmt.Errorf("GraphQL response exceeds %d bytes", maxGraphQLResponseBytes)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if resp.StatusCode != http.StatusOK {
		return nil, &gogithub.ErrorResponse{Response: resp, Message: string(body)}
	}
	if wire, ok := req.Context().Value(gqlResponseKey{}).(*gqlWireResponse); ok {
		if err := json.Unmarshal(body, wire); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func newGraphQLClient(token, host string, transport http.RoundTripper) *githubv4.Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient := &http.Client{
		Timeout:   requestTimeout,
		Transport: &gqlTransport{token: token, base: transport},
		// Authentication is applied by the transport; never forward it to a
		// redirect target, including a downgrade to plaintext HTTP.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if strings.EqualFold(host, "garage.github.com") {
		return githubv4.NewEnterpriseClient("https://garage.github.com/api/graphql", httpClient)
	}
	host = ghauth.NormalizeHostname(host)
	if host == defaultHost {
		return githubv4.NewClient(httpClient)
	}
	if ghauth.IsEnterprise(host) {
		return githubv4.NewEnterpriseClient("https://"+host+"/api/graphql", httpClient)
	}
	if host == "github.localhost" {
		return githubv4.NewEnterpriseClient("http://api.github.localhost/graphql", httpClient)
	}
	return githubv4.NewEnterpriseClient("https://api."+host+"/graphql", httpClient)
}

type gqlMutation struct {
	Query interface{}
	Input githubv4.Input
}

func queryGraphQL(ctx context.Context, client *githubv4.Client, query interface{}, variables map[string]interface{}, out interface{}) error {
	var wire gqlWireResponse
	ctx = context.WithValue(ctx, gqlResponseKey{}, &wire)
	var err error
	if mutation, ok := query.(gqlMutation); ok {
		err = client.Mutate(ctx, mutation.Query, mutation.Input, variables)
	} else {
		err = client.Query(ctx, query, variables)
	}
	if len(wire.Errors) > 0 {
		err = &gqlResponseError{Errors: wire.Errors}
	}
	if wire.Data != nil && (err == nil || isGraphQLResponseError(err)) {
		if decodeErr := json.Unmarshal(wire.Data, out); decodeErr != nil {
			return decodeErr
		}
	}
	return err
}

// Only alias counts and fixed schema selections enter these tags. All user
// values are variables, keeping reflect.StructOf's permanent type cache bounded.
func gqlField(name, selection string, value interface{}) reflect.StructField {
	typ := reflect.TypeOf(value)
	// githubv4 only traverses inline fragments that are struct values.
	if strings.HasPrefix(selection, "...") && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return reflect.StructField{Name: name, Type: typ, Tag: reflect.StructTag("graphql:" + strconv.Quote(selection))}
}

func gqlObject(fields ...reflect.StructField) interface{} {
	return reflect.New(reflect.StructOf(fields)).Interface()
}

func gqlRefQuery(owner, repo, qualified string, fields []reflect.StructField) (interface{}, map[string]interface{}) {
	query := gqlObject(
		gqlField("Repository", "repository(owner: $owner, name: $name)", gqlObject(
			gqlField("Ref", "ref(qualifiedName: $qualified)", gqlObject(fields...)),
		)),
		gqlField("RateLimit", "rateLimit", gqlRateLimit{}),
	)
	return query, map[string]interface{}{
		"owner": githubv4.String(owner), "name": githubv4.String(repo), "qualified": githubv4.String(qualified),
	}
}
