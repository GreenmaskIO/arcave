// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workload

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/statusreport"
)

// composeObservationReader reads the compose project's replicas as the daemon runs them.
type composeObservationReader interface {
	Observe(ctx context.Context) (composemodel.ObservedComposeProject, error)
}

// observer reads one compose project for a status.
type observer struct {
	reader composeObservationReader
}

// NewObserver builds the observer of the compose project reader reads.
func NewObserver(reader composeObservationReader) provider.Observer {
	return observer{reader: reader}
}

// Observe reads the compose project's replicas off the daemon.
func (o observer) Observe(ctx context.Context) (provider.ObservedWorkload, error) {
	observed, err := o.reader.Observe(ctx)
	if err != nil {
		return nil, err
	}
	return observedWorkload{
		observed: observed,
		logger:   *zerolog.Ctx(ctx),
	}, nil
}

// observedWorkload is the compose project as one read of the daemon found it.
type observedWorkload struct {
	observed composemodel.ObservedComposeProject
	logger   zerolog.Logger
}

// Deployment is the latest build the containers' labels name.
func (w observedWorkload) Deployment() *model.SavepointDeployment {
	return statusreport.FindLatestDeployment(w.observed)
}

// Report sets the replicas against the deployment and the deployed savepoint's record. A record
// whose body is not the compose provider's is logged and judged as none.
func (w observedWorkload) Report(checkedAt time.Time, deployed *model.Savepoint) model.WorkloadReport {
	var savepoint *composemodel.Savepoint
	if deployed != nil {
		parsed, err := composemodel.ParseSavepoint(*deployed)
		if err != nil {
			w.logger.Warn().Err(err).Msg("read the deployed savepoint's record as none")
		} else {
			savepoint = &parsed
		}
	}
	report := statusreport.NewReport(w.observed, checkedAt, savepoint)
	return model.WorkloadReport{
		State:   string(report.State),
		Notices: report.Notices,
		Detail: statusDetail{
			detail:    report.Detail,
			checkedAt: checkedAt,
		},
	}
}

// statusDetail is the compose provider's body of a status, its ages counted from checkedAt.
type statusDetail struct {
	detail    composemodel.StatusDetail
	checkedAt time.Time
}

// MarshalJSON encodes the body.
func (d statusDetail) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.detail)
}

// Table lists the services one per row.
func (d statusDetail) Table() model.Table {
	return statusreport.NewTable(d.detail, d.checkedAt)
}
