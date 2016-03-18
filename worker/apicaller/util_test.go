// Copyright 2015-2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apicaller_test

import (
	"github.com/juju/names"
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/agent"
	"github.com/juju/juju/api"
	"github.com/juju/juju/apiserver/params"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/worker"
	"github.com/juju/juju/worker/apicaller"
)

var errNotProvisioned = &params.Error{Code: params.CodeNotProvisioned}
var errNotAuthorized = &params.Error{Code: params.CodeUnauthorized}

type mockAgent struct {
	agent.Agent
	stub   *testing.Stub
	entity names.Tag
	model  names.ModelTag
}

func (mock *mockAgent) CurrentConfig() agent.Config {
	return dummyConfig{
		entity: mock.entity,
		model:  mock.model,
	}
}

func (mock *mockAgent) ChangeConfig(mutator agent.ConfigMutator) error {
	mock.stub.AddCall("ChangeConfig", mutator)
	return mock.stub.NextErr()
}

type dummyConfig struct {
	agent.Config
	entity names.Tag
	model  names.ModelTag
}

func (dummy dummyConfig) Tag() names.Tag {
	return dummy.entity
}

func (dummy dummyConfig) Model() names.ModelTag {
	return dummy.model
}

func (dummy dummyConfig) APIInfo() (*api.Info, bool) {
	return &api.Info{
		ModelTag: dummy.model,
		Tag:      dummy.entity,
		Password: "new",
	}, true
}

func (dummy dummyConfig) OldPassword() string {
	return "old"
}

type mockSetter struct {
	stub *testing.Stub
	agent.ConfigSetter
}

func (mock *mockSetter) Migrate(params agent.MigrateParams) error {
	mock.stub.AddCall("Migrate", params)
	return mock.stub.NextErr()
}

type mockConn struct {
	stub *testing.Stub
	api.Connection
	broken chan struct{}
}

func (mock *mockConn) ModelTag() (names.ModelTag, error) {
	mock.stub.AddCall("ModelTag")
	if err := mock.stub.NextErr(); err != nil {
		return names.ModelTag{}, err
	}
	return coretesting.ModelTag, nil
}

func (mock *mockConn) Broken() <-chan struct{} {
	return mock.broken
}

func (mock *mockConn) Close() error {
	mock.stub.AddCall("Close")
	return mock.stub.NextErr()
}

func (mock *mockConn) AgentConnection() apiagent.ConnectionFacade {
	return &mockAgentFacade{
		stub: mock.stub,
	}
}

type mockAgentFacade struct {
	stub *testing.Stub
	XXX
}

func (mock *mockAgentFacade) Life(entity names.Tag) (params.Life, error) {
	mock.stub.AddCall("Life", entity)
	if err := mock.stub.NextErr(); err != nil {
		return "", err
	}
	return mock.life, nil
}

type dummyWorker struct {
	worker.Worker
}

func assertStop(c *gc.C, w worker.Worker) {
	c.Assert(worker.Stop(w), jc.ErrorIsNil)
}

func assertStopError(c *gc.C, w worker.Worker, match string) {
	c.Assert(worker.Stop(w), gc.ErrorMatches, match)
}

func strategyTest(stub *testing.Stub, strategy utils.AttemptStrategy, test func(api.OpenFunc) (api.Connection, error)) (api.Connection, error) {
	unpatch := testing.PatchValue(apicaller.Strategy, strategy)
	defer unpatch()
	return test(func(info *api.Info, opts api.DialOpts) (api.Connection, error) {
		// copy because I don't trust what might happen to info
		stub.AddCall("apiOpen", *info, opts)
		err := stub.NextErr()
		if err != nil {
			return nil, err
		}
		return &mockConn{stub: stub}, nil
	})
}

func checkOpenCalls(c *gc.C, stub *testing.Stub, passwords ...string) {
	calls := openCalls(names.ModelTag{}, nil, passwords...)
	stub.CheckCalls(c, calls)
}

func openCalls(model names.ModelTag, entity names.Tag, passwords ...string) []testing.StubCall {
	calls := make([]testing.StubCall, len(passwords))
	for i, pw := range passwords {
		info := api.Info{
			ModelTag: model,
			Tag:      entity,
			Password: pw,
		}
		calls[i] = testing.StubCall{
			FuncName: "apiOpen",
			Args:     []interface{}{info, api.DialOpts{}},
		}
	}
	return calls
}
