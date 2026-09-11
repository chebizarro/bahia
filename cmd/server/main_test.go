package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

type gatedServerApplication struct {
	started        chan struct{}
	cancelObserved chan struct{}
	allowExit      chan struct{}
	exited         chan struct{}
	result         error
}

type inPlaceReloadApplication struct {
	*gatedServerApplication
	handled bool
	err     error
	called  chan *config.Config
}

func (a *inPlaceReloadApplication) ReloadConfig(candidate *config.Config) (bool, error) {
	a.called <- candidate
	return a.handled, a.err
}

func newGatedServerApplication() *gatedServerApplication {
	return &gatedServerApplication{
		started:        make(chan struct{}),
		cancelObserved: make(chan struct{}),
		exited:         make(chan struct{}),
	}
}

func (a *gatedServerApplication) RunContext(ctx context.Context) error {
	close(a.started)
	<-ctx.Done()
	close(a.cancelObserved)
	if a.allowExit != nil {
		<-a.allowExit
	}
	close(a.exited)
	return a.result
}

func TestRunWithDependenciesRetainsRunningApplicationWhenReloadConfigIsInvalid(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	reload := make(chan os.Signal, 1)
	logs := make(chan string, 1)
	application := newGatedServerApplication()
	loadCalls := 0

	done := make(chan error, 1)
	go func() {
		done <- runWithDependencies("config.yaml", serverDependencies{
			loadConfig: func(string) (*config.Config, error) {
				loadCalls++
				if loadCalls == 1 {
					return &config.Config{}, nil
				}
				return nil, errors.New("invalid candidate")
			},
			newApplication: func(*config.Config) (serverApplication, error) {
				return application, nil
			},
			newSignals: func() serverSignalSource {
				return serverSignalSource{root: root, reload: reload}
			},
			logf: func(format string, _ ...any) { logs <- format },
		})
	}()

	<-application.started
	reload <- os.Interrupt
	require.Contains(t, <-logs, "reload rejected")
	select {
	case <-application.cancelObserved:
		t.Fatal("invalid reload candidate canceled the running application")
	default:
	}

	cancelRoot()
	<-application.cancelObserved
	require.NoError(t, <-done)
}

func TestRunWithDependenciesRetainsRunningApplicationWhenReloadInitializationFails(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	reload := make(chan os.Signal, 1)
	logs := make(chan string, 1)
	application := newGatedServerApplication()
	factoryCalls := 0

	done := make(chan error, 1)
	go func() {
		done <- runWithDependencies("config.yaml", serverDependencies{
			loadConfig: func(string) (*config.Config, error) { return &config.Config{}, nil },
			newApplication: func(*config.Config) (serverApplication, error) {
				factoryCalls++
				if factoryCalls == 1 {
					return application, nil
				}
				return nil, errors.New("candidate initialization failed")
			},
			newSignals: func() serverSignalSource {
				return serverSignalSource{root: root, reload: reload}
			},
			logf: func(format string, _ ...any) { logs <- format },
		})
	}()

	<-application.started
	reload <- os.Interrupt
	require.Contains(t, <-logs, "initialization rejected")
	select {
	case <-application.cancelObserved:
		t.Fatal("failed candidate initialization canceled the running application")
	default:
	}

	cancelRoot()
	<-application.cancelObserved
	require.NoError(t, <-done)
}

func TestRunWithDependenciesAppliesSupportedReloadWithoutConstructingReplacement(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	reload := make(chan os.Signal, 1)
	application := &inPlaceReloadApplication{
		gatedServerApplication: newGatedServerApplication(),
		handled:                true,
		called:                 make(chan *config.Config, 1),
	}
	factoryCalls := 0
	loadCalls := 0
	logs := make(chan string, 1)

	done := make(chan error, 1)
	go func() {
		done <- runWithDependencies("config.yaml", serverDependencies{
			loadConfig: func(string) (*config.Config, error) {
				loadCalls++
				return &config.Config{Mode: "full"}, nil
			},
			newApplication: func(*config.Config) (serverApplication, error) {
				factoryCalls++
				return application, nil
			},
			newSignals: func() serverSignalSource {
				return serverSignalSource{root: root, reload: reload}
			},
			logf: func(format string, _ ...any) { logs <- format },
		})
	}()

	<-application.started
	reload <- os.Interrupt
	require.NotNil(t, <-application.called)
	require.Contains(t, <-logs, "applied in place")
	require.Equal(t, 1, factoryCalls)
	require.Equal(t, 2, loadCalls)
	select {
	case <-application.cancelObserved:
		t.Fatal("in-place reload canceled the running application")
	default:
	}

	cancelRoot()
	<-application.cancelObserved
	require.NoError(t, <-done)
}

func TestRunWithDependenciesStartsReplacementAfterOrderlyShutdown(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	reload := make(chan os.Signal, 1)
	current := newGatedServerApplication()
	current.allowExit = make(chan struct{})
	replacement := newGatedServerApplication()
	candidateConstructed := make(chan struct{})
	factoryCalls := 0

	done := make(chan error, 1)
	go func() {
		done <- runWithDependencies("config.yaml", serverDependencies{
			loadConfig: func(string) (*config.Config, error) { return &config.Config{}, nil },
			newApplication: func(*config.Config) (serverApplication, error) {
				factoryCalls++
				if factoryCalls == 1 {
					return current, nil
				}
				close(candidateConstructed)
				return replacement, nil
			},
			newSignals: func() serverSignalSource {
				return serverSignalSource{root: root, reload: reload}
			},
		})
	}()

	<-current.started
	reload <- os.Interrupt
	<-candidateConstructed
	<-current.cancelObserved
	select {
	case <-replacement.started:
		t.Fatal("replacement started before the current application completed shutdown")
	default:
	}

	close(current.allowExit)
	<-current.exited
	<-replacement.started

	cancelRoot()
	<-replacement.cancelObserved
	require.NoError(t, <-done)
}
