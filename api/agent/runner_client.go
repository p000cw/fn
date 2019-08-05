package agent

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opencensus.io/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/fnproject/fn/api/agent/grpc"
	"github.com/fnproject/fn/api/common"
	"github.com/fnproject/fn/api/models"
	pool "github.com/fnproject/fn/api/runnerpool"
	"github.com/fnproject/fn/grpcutil"

	pb_empty "github.com/golang/protobuf/ptypes/empty"
	"github.com/sirupsen/logrus"
)

var (
	ErrorRunnerClosed    = errors.New("Runner is closed")
	ErrorPureRunnerNoEOF = errors.New("Purerunner missing EOF response")
)

const (
	// max buffer size for grpc data messages, 10K
	MaxDataChunk = 10 * 1024
)

type gRPCRunner struct {
	shutWg  *common.WaitGroup
	address string
	conn    *grpc.ClientConn
	client  pb.RunnerProtocolClient
}

// implements Runner
func (r *gRPCRunner) Close(context.Context) error {
	r.shutWg.CloseGroup()
	return r.conn.Close()
}

func NewgRPCRunner(addr string, tlsConf *tls.Config, dialOpts ...grpc.DialOption) (pool.Runner, error) {
	conn, client, err := runnerConnection(addr, tlsConf, dialOpts...)
	if err != nil {
		return nil, err
	}

	return &gRPCRunner{
		shutWg:  common.NewWaitGroup(),
		address: addr,
		conn:    conn,
		client:  client,
	}, nil

}

func runnerConnection(address string, tlsConf *tls.Config, dialOpts ...grpc.DialOption) (*grpc.ClientConn, pb.RunnerProtocolClient, error) {

	ctx := context.Background()
	logger := common.Logger(ctx).WithField("runner_addr", address)
	ctx = common.WithLogger(ctx, logger)

	var creds credentials.TransportCredentials
	if tlsConf != nil {
		creds = credentials.NewTLS(tlsConf)
	}

	// we want to set a very short timeout to fail-fast if something goes wrong
	conn, err := grpcutil.DialWithBackoff(ctx, address, creds, 100*time.Millisecond, grpc.DefaultBackoffConfig, dialOpts...)
	if err != nil {
		logger.WithError(err).Error("Unable to connect to runner node")
	}

	protocolClient := pb.NewRunnerProtocolClient(conn)
	logger.Info("Connected to runner")

	return conn, protocolClient, nil
}

// implements Runner
func (r *gRPCRunner) Address() string {
	return r.address
}

// isTooBusy checks if the error is a retriable error (503) that is explicitly sent
// by runner. If isTooBusy returns true then we can idempotently run this call
// on the same or another runner.
func isTooBusy(err error) bool {
	// A formal API error returned from pure-runner
	if models.GetAPIErrorCode(err) == models.GetAPIErrorCode(models.ErrCallTimeoutServerBusy) {
		return true
	}
	if err != nil {
		// engagement/recv errors could also be a 503.
		st := status.Convert(err)
		if int(st.Code()) == models.GetAPIErrorCode(models.ErrCallTimeoutServerBusy) {
			return true
		}
	}
	return false
}

// TranslateGRPCStatusToRunnerStatus runner.RunnerStatus to runnerpool.RunnerStatus
func TranslateGRPCStatusToRunnerStatus(status *pb.RunnerStatus) *pool.RunnerStatus {
	if status == nil {
		return nil
	}

	// These are nanosecond monotonic deltas, they cannot be zero if they were transmitted.
	runnerSchedLatency := time.Duration(status.GetSchedulerDuration())
	runnerExecLatency := time.Duration(status.GetExecutionDuration())

	creat, _ := common.ParseDateTime(status.CreatedAt)
	start, _ := common.ParseDateTime(status.StartedAt)
	compl, _ := common.ParseDateTime(status.CompletedAt)

	return &pool.RunnerStatus{
		ActiveRequestCount: status.Active,
		RequestsReceived:   status.RequestsReceived,
		RequestsHandled:    status.RequestsHandled,
		StatusFailed:       status.Failed,
		KdumpsOnDisk:       status.KdumpsOnDisk,
		Cached:             status.Cached,
		StatusId:           status.Id,
		Details:            status.Details,
		ErrorCode:          status.ErrorCode,
		ErrorStr:           status.ErrorStr,
		CreatedAt:          creat,
		StartedAt:          start,
		CompletedAt:        compl,
		SchedulerDuration:  runnerSchedLatency,
		ExecutionDuration:  runnerExecLatency,
		IsNetworkDisabled:  status.IsNetworkDisabled,
	}
}

// implements Runner
func (r *gRPCRunner) Status(ctx context.Context) (*pool.RunnerStatus, error) {
	log := common.Logger(ctx).WithField("runner_addr", r.address)
	rid := common.RequestIDFromContext(ctx)
	if rid != "" {
		// Create a new gRPC metadata where we store the request ID
		mp := metadata.Pairs(common.RequestIDContextKey, rid)
		ctx = metadata.NewOutgoingContext(ctx, mp)
	}

	status, err := r.client.Status(ctx, &pb_empty.Empty{})
	log.WithError(err).Debugf("Status Call %+v", status)
	return TranslateGRPCStatusToRunnerStatus(status), err
}

// implements Runner
func (r *gRPCRunner) TryExec(ctx context.Context, call pool.RunnerCall) (bool, error) {
	log := common.Logger(ctx).WithField("runner_addr", r.address)

	log.Debug("Attempting to place call")
	if !r.shutWg.AddSession(1) {
		// try another runner if this one is closed.
		return false, ErrorRunnerClosed
	}
	defer r.shutWg.DoneSession()

	// extract the call's model data to pass on to the pure runner
	modelJSON, err := json.Marshal(call.Model())
	if err != nil {
		log.WithError(err).Error("Failed to encode model as JSON")
		// If we can't encode the model, no runner will ever be able to run this. Give up.
		return true, err
	}

	rid := common.RequestIDFromContext(ctx)
	if rid != "" {
		// Create a new gRPC metadata where we store the request ID
		mp := metadata.Pairs(common.RequestIDContextKey, rid)
		ctx = metadata.NewOutgoingContext(ctx, mp)
	}
	runnerConnection, err := r.client.Engage(ctx)
	if err != nil {
		// We are going to retry on a different runner, it is ok to log this error as Info
		log.WithError(err).Info("Unable to create client to runner node")
		// Try on next runner
		return false, err
	}

	err = runnerConnection.Send(&pb.ClientMsg{Body: &pb.ClientMsg_Try{Try: &pb.TryCall{
		ModelsCallJson: string(modelJSON),
		SlotHashId:     hex.EncodeToString([]byte(call.SlotHashId())),
		Extensions:     call.Extensions(),
	}}})
	if err != nil {
		// We are going to retry on a different runner, it is ok to log this error as Info
		log.WithError(err).Info("Failed to send message to runner node")
		// Let's ensure this is a codes.Unavailable error, otherwise we should
		// not assume that no data was transferred to the server. If the error is
		// retriable, then we can bubble up "not placed" to the caller to enable
		// a retry on this or different runner.
		isRetriable := status.Code(err) == codes.Unavailable
		return !isRetriable, err
	}

	// IMPORTANT: After this point TryCall was sent, we assume "COMMITTED" unless pure runner
	// send explicit NACK. Remember that requests may have no body and TryCall can contain all
	// data to execute a request.

	recvDone := make(chan error, 1)

	go receiveFromRunner(ctx, runnerConnection, r.address, call, recvDone)
	go sendToRunner(ctx, runnerConnection, r.address, call)

	select {
	case <-ctx.Done():
		log.Infof("Engagement Context ended ctxErr=%v", ctx.Err())
		return true, ctx.Err()
	case recvErr := <-recvDone:
		if isTooBusy(recvErr) {
			// Try on next runner
			return false, models.ErrCallTimeoutServerBusy
		}
		return true, recvErr
	}
}

func sendToRunner(ctx context.Context, protocolClient pb.RunnerProtocol_EngageClient, runnerAddress string, call pool.RunnerCall) {
	var errorMsg string
	var infoMsg string
	bodyReader := call.RequestBody()
	writeBuffer := make([]byte, MaxDataChunk)
	_, span := trace.StartSpan(ctx, "sendToRunner", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	log := common.Logger(ctx).WithField("runner_addr", runnerAddress)
	// IMPORTANT: IO Read below can fail in multiple go-routine cases (in retry
	// case especially if receiveFromRunner go-routine receives a NACK while sendToRunner is
	// already blocked on a read) or in the case of reading the http body multiple times (retries.)
	// Normally http.Request.Body can be read once. However runner_client users should implement/add
	// http.Request.GetBody() function and cache the body content in the request.
	// See lb_agent setRequestGetBody() which handles this. With GetBody installed,
	// the 'Read' below is an actually non-blocking operation since GetBody() should hand out
	// a new instance of io.ReadCloser() that allows repetitive reads on the http body.
	for {
		// WARNING: blocking read.
		n, err := bodyReader.Read(writeBuffer)
		if err != nil && err != io.EOF {
			errorMsg = fmt.Sprintf("Failed to receive data from http client body")
			span.SetStatus(trace.Status{Code: int32(trace.StatusCodeDataLoss), Message: errorMsg})
			log.WithError(err).Error(errorMsg)
		}

		// any IO error or n == 0 is an EOF for pure-runner
		isEOF := err != nil || n == 0
		data := writeBuffer[:n]
		infoMsg = fmt.Sprintf("Sending %d bytes of data isEOF=%v to runner", n, isEOF)
		span.Annotate([]trace.Attribute{trace.StringAttribute("status", infoMsg)}, "")
		log.Debugf(infoMsg)
		sendErr := protocolClient.Send(&pb.ClientMsg{
			Body: &pb.ClientMsg_Data{
				Data: &pb.DataFrame{
					Data: data,
					Eof:  isEOF,
				},
			},
		})
		if sendErr != nil {
			// It's often normal to receive an EOF here as we optimistically start sending body until a NACK
			// from the runner. Let's ignore EOF and rely on recv side to catch premature EOF.
			if sendErr != io.EOF {
				errorMsg = fmt.Sprintf("Failed to send data frame size=%d isEOF=%v", n, isEOF)
				span.SetStatus(trace.Status{Code: int32(trace.StatusCodeDataLoss), Message: errorMsg})
				log.WithError(sendErr).Errorf(errorMsg)
			}
			return
		}
		if isEOF {
			return
		}
	}
}

func parseError(msg *pb.CallFinished) error {
	if msg.GetSuccess() {
		return nil
	}
	eCode := msg.GetErrorCode()
	eStr := msg.GetErrorStr()
	if eStr == "" {
		eStr = "Unknown Error From Pure Runner"
	}
	err := models.NewAPIError(int(eCode), errors.New(eStr))
	if msg.GetErrorUser() {
		return models.NewFuncError(err)
	}
	return err
}

func tryQueueError(err error, done chan error) {
	select {
	case done <- err:
	default:
	}
}

func translateDate(dt string) time.Time {
	if dt != "" {
		trx, err := common.ParseDateTime(dt)
		if err == nil {
			return time.Time(trx)
		}
	}
	return time.Time{}
}

func recordFinishStats(ctx context.Context, msg *pb.CallFinished, c pool.RunnerCall) {

	// These are nanosecond monotonic deltas, they cannot be zero if they were transmitted.
	runnerSchedLatency := time.Duration(msg.GetSchedulerDuration())
	runnerExecLatency := time.Duration(msg.GetExecutionDuration())

	if runnerSchedLatency != 0 || runnerExecLatency != 0 {
		if runnerSchedLatency != 0 {
			statsLBAgentRunnerSchedLatency(ctx, runnerSchedLatency)
		}
		if runnerExecLatency != 0 {
			statsLBAgentRunnerExecLatency(ctx, runnerExecLatency)
			c.AddUserExecutionTime(runnerExecLatency)
		}
		return
	}

	// TODO: Remove this once all Runners are upgraded.
	// Fallback to older Runner response type, where instead of the above duration, formatted-wall-clock
	// timestamps are present.
	creatTs := translateDate(msg.GetCreatedAt())
	startTs := translateDate(msg.GetStartedAt())
	complTs := translateDate(msg.GetCompletedAt())

	// Validate this as info *is* coming from runner and its local clock.
	if !creatTs.IsZero() && !startTs.IsZero() && !complTs.IsZero() && !startTs.Before(creatTs) && !complTs.Before(startTs) {
		runnerSchedLatency := startTs.Sub(creatTs)
		runnerExecLatency := complTs.Sub(startTs)

		statsLBAgentRunnerSchedLatency(ctx, runnerSchedLatency)
		statsLBAgentRunnerExecLatency(ctx, runnerExecLatency)

		c.AddUserExecutionTime(runnerExecLatency)
	}
}

func cloneHeaders(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	return dst
}

func receiveFromRunner(ctx context.Context, protocolClient pb.RunnerProtocol_EngageClient, runnerAddress string, c pool.RunnerCall, done chan error) {
	var errorMsg string
	var infoMsg string
	w := c.ResponseWriter()
	defer close(done)
	ctx, span := trace.StartSpan(ctx, "receiveFromRunner", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	log := common.Logger(ctx).WithField("runner_addr", runnerAddress)
	statusCode := int32(0)
	// Make a copy of header to avoid concurrent read/write error when logCallFinish runs.
	clonedHeaders := cloneHeaders(w.Header())
	isPartialWrite := false

DataLoop:
	for {
		msg, err := protocolClient.Recv()
		if err != nil {
			log.WithError(err).Info("Receive error from runner")
			tryQueueError(err, done)
			return
		}

		switch body := msg.Body.(type) {

		// Process HTTP header/status message. This may not arrive depending on
		// pure runners behavior. (Eg. timeout & no IO received from function)
		case *pb.RunnerMsg_ResultStart:
			switch meta := body.ResultStart.Meta.(type) {
			case *pb.CallResultStart_Http:
				infoMsg = fmt.Sprintf("Received meta http result from runner Status=%v", meta.Http.StatusCode)
				span.Annotate([]trace.Attribute{trace.StringAttribute("status", infoMsg)}, "")
				log.Debugf(infoMsg)
				for _, header := range meta.Http.Headers {
					clonedHeaders.Set(header.Key, header.Value)
					w.Header().Set(header.Key, header.Value)
				}
				if meta.Http.StatusCode > 0 {
					statusCode = meta.Http.StatusCode
					w.WriteHeader(int(meta.Http.StatusCode))
				}
			default:
				errorMsg = fmt.Sprintf("Unhandled meta type in start message: %v", meta)
				span.SetStatus(trace.Status{Code: trace.StatusCodeDataLoss, Message: errorMsg})
				log.Errorf(errorMsg)
			}

		// May arrive if function has output. We ignore EOF.
		case *pb.RunnerMsg_Data:
			infoMsg = fmt.Sprintf("Received data from runner len=%d isEOF=%v", len(body.Data.Data), body.Data.Eof)
			span.Annotate([]trace.Attribute{trace.StringAttribute("status", infoMsg)}, "")
			log.Debugf(infoMsg)
			if !isPartialWrite {
				// WARNING: blocking write
				n, err := w.Write(body.Data.Data)
				if n != len(body.Data.Data) {
					isPartialWrite = true
					errorMsg = fmt.Sprintf("Failed to write full response (%d of %d) to client", n, len(body.Data.Data))
					span.SetStatus(trace.Status{Code: int32(trace.StatusCodeDataLoss), Message: errorMsg})
					log.WithError(err).Infof(errorMsg)
					if err == nil {
						err = io.ErrShortWrite
					}
					tryQueueError(err, done)
				}
			}

		// Finish messages required for finish/finalize the processing.
		case *pb.RunnerMsg_Finished:
			logCallFinish(log, body, clonedHeaders, statusCode)
			recordFinishStats(ctx, body.Finished, c)
			span.Annotate([]trace.Attribute{
				trace.BoolAttribute("ErrorUser", body.Finished.GetErrorUser()),
				trace.BoolAttribute("Success", body.Finished.GetSuccess()),
				trace.Int64Attribute("ExecutionDuration", body.Finished.GetExecutionDuration()),
				trace.Int64Attribute("SchedulerDuration", body.Finished.GetSchedulerDuration()),
				trace.Int64Attribute("DockerWaitDuration", body.Finished.GetDockerWaitDuration()),
				trace.Int64Attribute("DockerPullDuration", body.Finished.GetDockerPullDuration()),
				trace.Int64Attribute("DockerPullRetries", int64(body.Finished.GetDockerPullRetries())),
				trace.StringAttribute("CompletedAt", body.Finished.GetCompletedAt()),
				trace.StringAttribute("CreatedAt", body.Finished.GetCreatedAt()),
				trace.StringAttribute("StartedAt", body.Finished.GetStartedAt()),
			}, "Runner Execution Details")
			span.AddAttributes(
				trace.StringAttribute("image", body.Finished.GetImage()),
				trace.StringAttribute("fn.call_id", body.Finished.GetDetails()),
			)
			span.SetStatus(trace.Status{Code: body.Finished.GetErrorCode(), Message: body.Finished.GetErrorStr()})
			if !body.Finished.Success {
				err := parseError(body.Finished)
				tryQueueError(err, done)
			}
			break DataLoop

		default:
			errorMsg = fmt.Sprintf("Ignoring unknown message type %T from runner, possible client/server mismatch", body)
			span.SetStatus(trace.Status{Code: trace.StatusCodeUnauthenticated, Message: errorMsg})
			log.Errorf(errorMsg)
		}
	}

	// There should be an EOF following the last packet
	for {
		msg, err := protocolClient.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.WithError(err).Infof("Call Waiting EOF received error")
			tryQueueError(err, done)
			break
		}

		switch body := msg.Body.(type) {
		default:
			log.Infof("Call Waiting EOF ignoring message %T", body)
		}
		tryQueueError(ErrorPureRunnerNoEOF, done)
	}
}

func logCallFinish(log logrus.FieldLogger, msg *pb.RunnerMsg_Finished, headers http.Header, httpStatus int32) {
	errorCode := msg.Finished.GetErrorCode()
	errorUser := msg.Finished.GetErrorUser()
	runnerSuccess := msg.Finished.GetSuccess()
	logger := log.WithFields(logrus.Fields{
		"function_error":     msg.Finished.GetErrorStr(),
		"runner_success":     runnerSuccess,
		"runner_error_code":  errorCode,
		"runner_error_user":  errorUser,
		"runner_http_status": httpStatus,
		"fn_http_status":     headers.Get("Fn-Http-Status"),
	})
	if !runnerSuccess && !errorUser && errorCode != http.StatusServiceUnavailable {
		logger.Warn("Call finished")
	} else {
		logger.Info("Call finished")
	}
}

var _ pool.Runner = &gRPCRunner{}
