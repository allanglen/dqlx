package dqlx

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dgraph-io/dgo/v200"
	"github.com/dgraph-io/dgo/v200/protos/api"
	"github.com/mitchellh/mapstructure"
	"reflect"
	"time"
)

type DGoExecutor struct {
	client *dgo.Dgraph
	tnx    *dgo.Txn

	readOnly   bool
	bestEffort bool
}

type ExecutorOptionFn func(executor *DGoExecutor)

func WithTnx(tnx *dgo.Txn) ExecutorOptionFn {
	return func(executor *DGoExecutor) {
		executor.tnx = tnx
	}
}

func WithClient(client *dgo.Dgraph) ExecutorOptionFn {
	return func(executor *DGoExecutor) {
		executor.client = client
	}
}

func WithReadOnly(readOnly bool) ExecutorOptionFn {
	return func(executor *DGoExecutor) {
		executor.readOnly = readOnly
	}
}

func WithBestEffort(bestEffort bool) ExecutorOptionFn {
	return func(executor *DGoExecutor) {
		executor.bestEffort = bestEffort
	}
}

func NewDGoExecutor(client *dgo.Dgraph) *DGoExecutor {
	return &DGoExecutor{
		client: client,
	}
}

func (executor DGoExecutor) ExecuteQueries(ctx context.Context, queries ...QueryBuilder) (*Response, error) {
	if err := executor.ensureClient(); err != nil {
		return nil, err
	}

	query, variables, err := QueriesToDQL(queries...)
	if err != nil {
		return nil, err
	}

	tx := executor.getTnx()

	defer tx.Discard(ctx)

	resp, err := tx.QueryWithVars(ctx, query, variables)
	if err != nil {
		return nil, err
	}

	if !executor.readOnly {
		err := tx.Commit(ctx)
		if err != nil {
			return nil, err
		}
	}

	return executor.toResponse(resp, queries...)
}

func (executor DGoExecutor) ExecuteMutations(ctx context.Context, mutations ...MutationBuilder) (*Response, error) {
	if err := executor.ensureClient(); err != nil {
		return nil, err
	}

	var queries []QueryBuilder
	var mutationRequests []*api.Mutation

	for _, mutation := range mutations {
		var condition string

		if mutation.condition != nil {
			conditionDql, _, err := mutation.condition.ToDQL()
			if err != nil {
				return nil, err
			}
			condition = conditionDql
		}

		queries = append(queries, mutation.query)
		setData, deleteData, err := mutationData(mutation)

		if err != nil {
			return nil, err
		}

		mutationRequest := &api.Mutation{
			SetJson:    setData,
			DeleteJson: deleteData,
			Cond:       condition,
			CommitNow:  executor.tnx == nil,
		}

		mutationRequests = append(mutationRequests, mutationRequest)
	}

	query, variables, err := QueriesToDQL(queries...)

	if IsEmptyQuery(query) {
		query = ""
		variables = nil
	}

	request := &api.Request{
		Query:      query,
		Vars:       variables,
		ReadOnly:   executor.readOnly,
		BestEffort: executor.bestEffort,
		Mutations:  mutationRequests,
		CommitNow:  executor.tnx == nil,
		RespFormat: api.Request_JSON,
	}

	tx := executor.getTnx()
	defer tx.Discard(ctx)

	resp, err := tx.Do(ctx, request)

	if err != nil {
		return nil, err
	}

	return executor.toResponse(resp, queries...)
}

func (executor DGoExecutor) toResponse(resp *api.Response, queries ...QueryBuilder) (*Response, error) {
	var dataPathKey string

	if len(queries) == 1 {
		dataPathKey = queries[0].rootEdge.Name
	} else {
		dataPathKey = ""
	}

	queryResponse := &Response{
		dataKeyPath: dataPathKey,
		Raw:         resp,
	}

	queries = ensureUniqueQueryNames(queries)

	for _, queryBuilder := range queries {
		if queryBuilder.unmarshalInto == nil {
			continue
		}
		singleResponse := &Response{
			dataKeyPath: queryBuilder.rootEdge.Name,
			Raw:         resp,
		}

		err := singleResponse.Unmarshal(queryBuilder.unmarshalInto)

		if err != nil {
			return nil, err
		}
	}

	return queryResponse, nil
}

func mutationData(mutation MutationBuilder) (updateData []byte, deleteData []byte, err error) {
	var setDataBytes []byte
	var deleteDataBytes []byte

	if mutation.setData != nil {
		setBytes, err := json.Marshal(mutation.setData)
		if err != nil {
			return nil, nil, err
		}
		setDataBytes = setBytes
	}

	if mutation.delData != nil {
		deleteBytes, err := json.Marshal(mutation.delData)
		if err != nil {
			return nil, nil, err
		}
		deleteDataBytes = deleteBytes
	}

	return setDataBytes, deleteDataBytes, nil
}

func (executor DGoExecutor) ensureClient() error {
	if executor.client == nil {
		return errors.New("cannot execute query without setting a dqlx. use DClient() to set one")
	}
	return nil
}

func (executor DGoExecutor) getTnx() *dgo.Txn {
	tx := executor.tnx

	if tx == nil {
		if executor.readOnly {
			tx = executor.client.NewReadOnlyTxn()
		} else {
			tx = executor.client.NewTxn()
		}
	}
	return tx
}

type Response struct {
	Raw         *api.Response
	dataKeyPath string
}

func (response Response) Unmarshal(value interface{}) error {
	values := map[string]interface{}{}
	err := json.Unmarshal(response.Raw.Json, &values)

	if err != nil {
		return err
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: func(from reflect.Value, to reflect.Value) (interface{}, error) {
			if _, ok := to.Interface().(time.Time); ok {
				return time.Parse(time.RFC3339, from.String())
			}
			return from.Interface(), nil
		},
		Result:  value,
		TagName: "json",
	})

	if err != nil {
		return err
	}

	if response.dataKeyPath != "" {
		return decoder.Decode(values[response.dataKeyPath])
	}

	return decoder.Decode(values)
}