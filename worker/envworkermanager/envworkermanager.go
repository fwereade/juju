// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package envworkermanager

import (
	"time"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/names"
	"gopkg.in/mgo.v2"
	"launchpad.net/tomb"

	cmdutil "github.com/juju/juju/cmd/jujud/util"
	"github.com/juju/juju/state"
	"github.com/juju/juju/worker"
)

var logger = loggo.GetLogger("juju.worker.envworkermanager")

type envWorkersCreator func(InitialState, *state.State) (worker.Worker, error)

// NewEnvWorkerManager returns a Worker which manages a worker which
// needs to run on a per environment basis. It takes a function which will
// be called to start a worker for a new environment. This worker
// will be killed when an environment goes away.
func NewEnvWorkerManager(
	st InitialState,
	startEnvWorker envWorkersCreator,
	dyingEnvWorker envWorkersCreator,
	delay time.Duration,
) worker.Worker {
	m := &envWorkerManager{
		st:             st,
		startEnvWorker: startEnvWorker,
		dyingEnvWorker: dyingEnvWorker,
	}
	m.runner = worker.NewRunner(cmdutil.IsFatal, cmdutil.MoreImportant, delay)
	go func() {
		defer m.tomb.Done()
		m.tomb.Kill(m.loop())
	}()
	return m
}

// InitialState defines the State functionality used by
// envWorkerManager and/or could be useful to startEnvWorker
// funcs. It mainly exists to support testing.
type InitialState interface {
	WatchEnvironments() state.StringsWatcher
	ForEnviron(names.ModelTag) (*state.State, error)
	GetEnvironment(names.ModelTag) (*state.Model, error)
	ModelUUID() string
	Machine(string) (*state.Machine, error)
	MongoSession() *mgo.Session
}

type envWorkerManager struct {
	runner         worker.Runner
	tomb           tomb.Tomb
	st             InitialState
	startEnvWorker envWorkersCreator
	dyingEnvWorker envWorkersCreator
}

// Kill satisfies the Worker interface.
func (m *envWorkerManager) Kill() {
	m.tomb.Kill(nil)
}

// Wait satisfies the Worker interface.
func (m *envWorkerManager) Wait() error {
	return m.tomb.Wait()
}

func (m *envWorkerManager) loop() error {
	go func() {
		// When the runner stops, make sure we stop the envWorker as well
		m.tomb.Kill(m.runner.Wait())
	}()
	defer func() {
		// When we return, make sure that we kill
		// the runner and wait for it.
		m.runner.Kill()
		m.tomb.Kill(m.runner.Wait())
	}()
	w := m.st.WatchEnvironments()
	defer w.Stop()
	for {
		select {
		case uuids := <-w.Changes():
			// One or more environments have changed.
			for _, uuid := range uuids {
				if err := m.envHasChanged(uuid); err != nil {
					return errors.Trace(err)
				}
			}
		case <-m.tomb.Dying():
			return tomb.ErrDying
		}
	}
}

func (m *envWorkerManager) envHasChanged(uuid string) error {
	modelTag := names.NewModelTag(uuid)
	env, err := m.st.GetEnvironment(modelTag)
	if errors.IsNotFound(err) {
		return m.envNotFound(modelTag)
	} else if err != nil {
		return errors.Annotatef(err, "error loading model %s", modelTag.Id())
	}

	switch env.Life() {
	case state.Alive:
		err = m.envIsAlive(modelTag)
	case state.Dying:
		err = m.envIsDying(modelTag)
	case state.Dead:
		err = m.envIsDead(modelTag)
	}

	return errors.Trace(err)
}

func (m *envWorkerManager) envIsAlive(modelTag names.ModelTag) error {
	return m.runner.StartWorker(modelTag.Id(), func() (worker.Worker, error) {
		st, err := m.st.ForEnviron(modelTag)
		if err != nil {
			return nil, errors.Annotatef(err, "failed to open state for model %s", modelTag.Id())
		}
		closeState := func() {
			err := st.Close()
			if err != nil {
				logger.Errorf("error closing state for model %s: %v", modelTag.Id(), err)
			}
		}

		envRunner, err := m.startEnvWorker(m.st, st)
		if err != nil {
			closeState()
			return nil, errors.Trace(err)
		}

		// Close State when the runner for the environment is done.
		go func() {
			envRunner.Wait()
			closeState()
		}()

		return envRunner, nil
	})
}

func dyingEnvWorkerId(uuid string) string {
	return "dying" + ":" + uuid
}

// envNotFound stops all workers for that environment.
func (m *envWorkerManager) envNotFound(modelTag names.ModelTag) error {
	uuid := modelTag.Id()
	if err := m.runner.StopWorker(uuid); err != nil {
		return errors.Trace(err)
	}
	if err := m.runner.StopWorker(dyingEnvWorkerId(uuid)); err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (m *envWorkerManager) envIsDying(modelTag names.ModelTag) error {
	id := dyingEnvWorkerId(modelTag.Id())
	return m.runner.StartWorker(id, func() (worker.Worker, error) {
		st, err := m.st.ForEnviron(modelTag)
		if err != nil {
			return nil, errors.Annotatef(err, "failed to open state for model %s", modelTag.Id())
		}
		closeState := func() {
			err := st.Close()
			if err != nil {
				logger.Errorf("error closing state for model %s: %v", modelTag.Id(), err)
			}
		}

		dyingRunner, err := m.dyingEnvWorker(m.st, st)
		if err != nil {
			closeState()
			return nil, errors.Trace(err)
		}

		// Close State when the runner for the environment is done.
		go func() {
			dyingRunner.Wait()
			closeState()
		}()

		return dyingRunner, nil
	})
}

func (m *envWorkerManager) envIsDead(modelTag names.ModelTag) error {
	uuid := modelTag.Id()
	err := m.runner.StopWorker(uuid)
	if err != nil {
		return errors.Trace(err)
	}

	return nil
}
