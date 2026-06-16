package transport

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/utils/ioutil"
)

// ReceivePackRequest is a set of options for the ReceivePack service.
type ReceivePackRequest struct {
	GitProtocol   string
	AdvertiseRefs bool
	StatelessRPC  bool

	// Hooks, if set, are invoked between packfile unpack and ref update
	// (PreReceive) and after ref update (PostReceive). A non-nil PreReceive
	// error rejects every ref in the push with that error as the
	// report-status reason; refs are not updated.
	Hooks *ReceivePackHooks
}

// ReceivePackHooks holds server-side callbacks for ReceivePack.
type ReceivePackHooks struct {
	// PreReceive runs after the packfile is unpacked into st but before any
	// ref is updated. cmds are the proposed updates. progress writes to the
	// sideband progress channel (band 2) when sideband is negotiated, or is
	// discarded otherwise; write human-readable lines for the client's
	// `remote:` output. Returning a non-nil error refuses every ref with
	// err.Error() as the report-status reason.
	PreReceive func(ctx context.Context, st storage.Storer, cmds []*packp.Command, progress io.Writer) error

	// PostReceive runs after refs are updated. It is best-effort; errors are
	// not surfaced to the client.
	PostReceive func(ctx context.Context, st storage.Storer, cmds []*packp.Command)
}

// ReceivePack is a server command that serves the receive-pack service.
func ReceivePack(
	ctx context.Context,
	st storage.Storer,
	r io.ReadCloser,
	w io.WriteCloser,
	opts *ReceivePackRequest,
) error {
	if w == nil {
		return fmt.Errorf("nil writer")
	}

	w = ioutil.NewContextWriteCloser(ctx, w)

	if opts == nil {
		opts = &ReceivePackRequest{}
	}

	if opts.AdvertiseRefs || !opts.StatelessRPC {
		switch version := ProtocolVersion(opts.GitProtocol); version {
		case protocol.V1:
			if _, err := pktline.Writef(w, "version %d\n", version); err != nil {
				return err
			}
		// TODO: support version 2
		case protocol.V0, protocol.V2:
		default:
			return fmt.Errorf("%w: %q", ErrUnsupportedVersion, version)
		}

		if err := AdvertiseRefs(ctx, st, w, ReceivePackService, opts.StatelessRPC); err != nil {
			return err
		}
	}

	if opts.AdvertiseRefs {
		// Done, there's nothing else to do
		return nil
	}

	if r == nil {
		return fmt.Errorf("nil reader")
	}

	r = ioutil.NewContextReadCloser(ctx, r)

	rd := bufio.NewReader(r)
	l, _, err := pktline.PeekLine(rd)
	if err != nil {
		return err
	}

	// At this point, if we get a flush packet, it means the client
	// has nothing to send, so we can return early.
	if l == pktline.Flush {
		return nil
	}

	updreq := &packp.UpdateRequests{}
	if err := updreq.Decode(rd); err != nil {
		return err
	}

	var (
		caps         = updreq.Capabilities
		needPackfile bool
		pushOpts     packp.PushOptions
	)

	// TODO: Pass the options to the server-side hooks.
	if updreq.Capabilities.Supports(capability.PushOptions) {
		if err := pushOpts.Decode(rd); err != nil {
			return fmt.Errorf("decoding push-options: %w", err)
		}
	}

	// Should we expect a packfile?
	for _, cmd := range updreq.Commands {
		if cmd.Action() != packp.Delete {
			needPackfile = true
			break
		}
	}

	// Receive the packfile
	var unpackErr error
	if needPackfile {
		unpackErr = packfile.UpdateObjectStorage(st, rd)
	}

	// Done with the request, now close the reader
	// to indicate that we are done reading from it.
	if err := r.Close(); err != nil {
		return fmt.Errorf("closing reader: %w", err)
	}

	wantReport := updreq.Capabilities.Supports(capability.ReportStatus)

	var (
		useSideband bool
		writer      io.Writer = w
		progress    io.Writer = io.Discard
	)
	if !caps.Supports(capability.NoProgress) {
		var mux *sideband.Muxer
		if caps.Supports(capability.Sideband64k) {
			mux = sideband.NewMuxer(sideband.Sideband64k, w)
		} else if caps.Supports(capability.Sideband) {
			mux = sideband.NewMuxer(sideband.Sideband, w)
		}
		if mux != nil {
			writer = mux
			progress = sidebandProgress{mux}
			useSideband = true
		}
	}

	writeCloser := ioutil.NewWriteCloser(writer, w)
	if unpackErr != nil {
		if !wantReport {
			return unpackErr
		}
		res := sendReportStatus(writeCloser, unpackErr, nil)
		_ = closeWriter(w)
		return res
	}

	var firstErr error
	cmdStatus := make(map[plumbing.ReferenceName]error)

	if opts.Hooks != nil && opts.Hooks.PreReceive != nil {
		if err := opts.Hooks.PreReceive(ctx, st, updreq.Commands, progress); err != nil {
			for _, cmd := range updreq.Commands {
				setStatus(cmdStatus, &firstErr, cmd.Name, err)
			}
		}
	}

	if firstErr == nil {
		updateReferences(st, updreq, cmdStatus, &firstErr)
		if opts.Hooks != nil && opts.Hooks.PostReceive != nil {
			opts.Hooks.PostReceive(ctx, st, updreq.Commands)
		}
	}

	if !wantReport {
		return firstErr
	}

	if err := sendReportStatus(writeCloser, nil, cmdStatus); err != nil {
		return err
	}

	if useSideband {
		if err := pktline.WriteFlush(w); err != nil {
			return fmt.Errorf("flushing sideband: %w", err)
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return closeWriter(w)
}

type sidebandProgress struct{ mux *sideband.Muxer }

func (p sidebandProgress) Write(b []byte) (int, error) {
	return p.mux.WriteChannel(sideband.ProgressMessage, b)
}

func closeWriter(w io.WriteCloser) error {
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing writer: %w", err)
	}
	return nil
}

func sendReportStatus(w io.WriteCloser, unpackErr error, cmdStatus map[plumbing.ReferenceName]error) error {
	rs := &packp.ReportStatus{}
	rs.UnpackStatus = "ok"
	if unpackErr != nil {
		rs.UnpackStatus = unpackErr.Error()
	}

	for ref, err := range cmdStatus {
		msg := "ok"
		if err != nil {
			msg = err.Error()
		}
		status := &packp.CommandStatus{
			ReferenceName: ref,
			Status:        msg,
		}
		rs.CommandStatuses = append(rs.CommandStatuses, status)
	}

	if err := rs.Encode(w); err != nil {
		return err
	}

	return nil
}

func setStatus(cmdStatus map[plumbing.ReferenceName]error, firstErr *error, ref plumbing.ReferenceName, err error) {
	cmdStatus[ref] = err
	if *firstErr == nil && err != nil {
		*firstErr = err
	}
}

func referenceExists(s storer.ReferenceStorer, n plumbing.ReferenceName) (bool, error) {
	_, err := s.Reference(n)
	if err == plumbing.ErrReferenceNotFound {
		return false, nil
	}

	return err == nil, err
}

func updateReferences(st storage.Storer, req *packp.UpdateRequests, cmdStatus map[plumbing.ReferenceName]error, firstErr *error) {
	for _, cmd := range req.Commands {
		exists, err := referenceExists(st, cmd.Name)
		if err != nil {
			setStatus(cmdStatus, firstErr, cmd.Name, err)
			continue
		}

		switch cmd.Action() {
		case packp.Create:
			if exists {
				setStatus(cmdStatus, firstErr, cmd.Name, ErrUpdateReference)
				continue
			}

			ref := plumbing.NewHashReference(cmd.Name, cmd.New)
			err := st.SetReference(ref)
			setStatus(cmdStatus, firstErr, cmd.Name, err)
		case packp.Delete:
			if !exists {
				setStatus(cmdStatus, firstErr, cmd.Name, ErrUpdateReference)
				continue
			}

			err := st.RemoveReference(cmd.Name)
			setStatus(cmdStatus, firstErr, cmd.Name, err)
		case packp.Update:
			if !exists {
				setStatus(cmdStatus, firstErr, cmd.Name, ErrUpdateReference)
				continue
			}

			ref := plumbing.NewHashReference(cmd.Name, cmd.New)
			err := st.SetReference(ref)
			setStatus(cmdStatus, firstErr, cmd.Name, err)
		}
	}
}
