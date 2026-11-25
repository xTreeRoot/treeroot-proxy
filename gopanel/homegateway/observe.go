package homegateway

import (
	"context"
	"time"
)

// ObserveLocal validates connection telemetry in the isolated lab without a control server.
func (a *Agent) ObserveLocal(ctx context.Context, duration time.Duration) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	queue := make(chan Event, 1024)
	go a.stream(ctx, queue)
	result := []Event{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-queue:
			for _, p := range a.State.Desired.Devices {
				if p.FixedIP == e.Source {
					e.MAC = p.MAC
					break
				}
			}
			if e.MAC != "" {
				result = append(result, e)
			}
		case <-tick.C:
			if e := a.sample(ctx); e != nil {
				return nil, e
			}
		case <-ctx.Done():
			return result, nil
		}
	}
}
