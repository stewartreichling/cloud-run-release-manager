package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/config"
	"github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/metrics"
	"github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/metrics/sheets"
	"github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/metrics/stackdriver"
	"github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/rollout"
	runapi "github.com/GoogleCloudPlatform/cloud-run-release-manager/internal/run"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// runRollouts concurrently handles the rollout of the targeted services.
func runRollouts(ctx context.Context, logger *logrus.Logger, strategy config.Strategy) []error {
	svcs, err := getTargetedServices(ctx, logger, strategy.Target)
	if err != nil {
		return []error{errors.Wrap(err, "failed to get targeted services")}
	}
	if len(svcs) == 0 {
		logger.Warn("no service matches the targets")
	}

	var (
		errs []error
		mu   sync.Mutex
		wg   sync.WaitGroup
	)
	for _, svc := range svcs {
		wg.Add(1)
		go func(ctx context.Context, lg *logrus.Logger, svc *rollout.ServiceRecord, strategy config.Strategy) {
			defer wg.Done()
			err := handleRollout(ctx, lg, svc, strategy)
			if err != nil {
				lg.Debugf("rollout error for service %q: %+v", svc.Service.Metadata.Name, err)
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(ctx, logger, svc, strategy)
	}
	wg.Wait()

	return errs
}

// handleRollout manages the rollout process for a single service.
func handleRollout(ctx context.Context, logger *logrus.Logger, service *rollout.ServiceRecord, strategy config.Strategy) error {
	lg := logger.WithFields(logrus.Fields{
		"project": service.Project,
		"service": service.Metadata.Name,
		"region":  service.Region,
	})

	client, err := runapi.NewAPIClient(ctx, service.Region)
	if err != nil {
		return errors.Wrap(err, "failed to initialize Cloud Run API client")
	}
	metricsProvider, err := chooseMetricsProvider(ctx, lg, service.Project, service.Region, service.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to initialize metrics provider")
	}
	roll := rollout.New(ctx, metricsProvider, service, strategy).WithClient(client).WithLogger(lg.Logger)

	changed, err := roll.Rollout()
	if err != nil {
		lg.Errorf("rollout failed, error=%v", err)
		return errors.Wrap(err, "rollout failed")
	}

	if changed {
		lg.Info("service was successfully updated")
	} else {
		lg.Debug("service kept unchanged")
	}
	return nil
}

// rolloutErrsToString returns the string representation of all the errors found
// during the rollout of all targeted services.
func rolloutErrsToString(errs []error) (errsStr string) {
	for i, err := range errs {
		if i > 0 {
			errsStr += "\n"
		}
		errsStr += fmt.Sprintf("[error#%d] %v", i, err)
	}
	return errsStr
}

// chooseMetricsProvider checks the CLI flags and determine which metrics
// provider should be used for the rollout.
func chooseMetricsProvider(ctx context.Context, logger *logrus.Entry, project, region, svcName string) (metrics.Provider, error) {
	if flGoogleSheetsID != "" {
		logger.Debug("using Google Sheets as metrics provider")
		return sheets.NewProvider(ctx, flGoogleSheetsID, "", region, svcName)
	}
	logger.Debug("using Cloud Monitoring (Stackdriver) as metrics provider")
	return stackdriver.NewProvider(ctx, project, region, svcName)
}
