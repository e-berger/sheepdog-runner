package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/e-berger/sheepdog-runner/internal/infra"
	"github.com/e-berger/sheepdog-runner/internal/infra/messaging"
	"github.com/e-berger/sheepdog-runner/internal/metrics"
	"github.com/e-berger/sheepdog-runner/internal/probes"
	"github.com/e-berger/sheepdog-runner/internal/status"
)

type Controller struct {
	pushGateway    *metrics.Push
	queueMessaging *messaging.Messaging
	ctx            context.Context
}

func NewController(ctx context.Context, pushGateway string, sqsQueueName string) (*Controller, error) {
	var p *metrics.Push
	if pushGateway != "" {
		p = metrics.NewPush(pushGateway)
	}

	var m *messaging.Messaging
	if sqsQueueName != "" {
		slog.Info("Using messaging", "queue", sqsQueueName)
		cfg, err := infra.NewSession()
		if err != nil {
			return nil, err
		}
		clientSqs := sqs.NewFromConfig(*cfg)
		m = messaging.NewMessaging(clientSqs, sqsQueueName)
		m.Start(ctx)
	}

	return &Controller{
		pushGateway:    p,
		queueMessaging: m,
		ctx:            ctx,
	}, nil
}

func (c *Controller) Run(probesDatas []probes.IProbe) {
	monitorErr := 0
	wg := new(sync.WaitGroup)
	for _, probe := range probesDatas {
		wg.Add(1)
		go c.runProbe(probe, wg, &monitorErr)
	}
	wg.Wait()
	slog.Info("End monitoring", "nb error", monitorErr)
}

func (c *Controller) runProbe(probe probes.IProbe, wg *sync.WaitGroup, monitorErr *int) {
	slog.Info("Launching monitoring", "probe", probe.String())
	defer wg.Done()
	result, err := probe.Launch()
	if err != nil {
		*monitorErr++
		slog.Error("Error launching monitoring", "error", err)
	} else {
		err = c.SendMetrics(result)
		if err != nil {
			*monitorErr++
			slog.Error("Error pushing monitoring", "error", err)
		}
	}
	if c.queueMessaging != nil {
		errStatus := c.UpdateProbeStatus(probe, result.GetTime(), err)
		if errStatus != nil {
			slog.Error("Error publishing status", "error", errStatus)
		}
	} else {
		slog.Info("No queue messaging defined")
	}
}

func (c *Controller) SendMetrics(metrics metrics.IMetrics) error {
	var err error
	if c.pushGateway != nil {
		slog.Info("Metrics monitoring", "probe", metrics.String())
		err = c.pushGateway.Send(metrics.GetId(), metrics.GetMetrics())
		if err != nil {
			slog.Error("Error pushing monitoring", "error", err)
		}
	} else {
		slog.Info("No push gateway defined")
	}
	return err
}

func (c *Controller) UpdateProbeStatus(probe probes.IProbe, started time.Time, err error) error {
	//detect if probe status has changed
	if err != nil || (err == nil && probe.IsError()) {
		slog.Info("Update status : needed", "probe", probe.IsError(), "error", err)
		var s *status.Status
		if err != nil {
			s = status.NewStatus(started, probe.GetId(), probes.ERROR, err.Error(), probe.GetMode())
		} else {
			s = status.NewStatus(started, probe.GetId(), probes.UP, "", probe.GetMode())
		}
		return c.queueMessaging.Publish(c.ctx, s)
	}
	slog.Info("Update status : not needed", "probe", probe.IsError(), "error", err)
	return nil
}
