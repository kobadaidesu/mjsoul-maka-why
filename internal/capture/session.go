package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type transport interface {
	Read(context.Context, *cdproto.Message) error
	Write(context.Context, *cdproto.Message) error
	Close() error
}

type packet struct {
	message cdproto.Message
	err     error
}

// Observe uses chromedp's low-level transport only. It never starts Chrome,
// creates a target, enables Runtime/Page, or runs chromedp browser actions.
func Observe(ctx context.Context, target Target, sink Sink, policy BodyPolicy, after func(Event), ready func()) error {
	if !target.validated {
		return fmt.Errorf("CDP target must first pass SelectTarget endpoint validation")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := chromedp.DialContext(dialCtx, target.WebSocketDebuggerURL)
	cancel()
	if err != nil {
		return fmt.Errorf("attach existing CDP target: %w", err)
	}
	return run(ctx, conn, sink, policy, after, ready)
}

func run(ctx context.Context, conn transport, sink Sink, policy BodyPolicy, after func(Event), ready func()) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	if policy.MaxBodyBytes <= 0 || policy.MaxTotalBytes < policy.MaxBodyBytes || policy.MaxTotalBytes > 1<<30 {
		cancel()
		return errors.Join(fmt.Errorf("invalid HTTP body budgets"), conn.Close())
	}
	packets := make(chan packet, 64)
	closeResult := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	// chromedp.Conn.Read/Write ignore Context; closing this dedicated socket
	// interrupts them. The watcher and reader are both joined before return.
	go func() {
		defer wg.Done()
		<-ctx.Done()
		closeResult <- conn.Close()
	}()
	go func() {
		defer wg.Done()
		defer close(packets) // The reader owns channel closure.
		for {
			var msg cdproto.Message
			readErr := conn.Read(ctx, &msg)
			// A completed read is always handed off, even during cancellation;
			// shutdown drains this channel before joining the goroutines.
			packets <- packet{message: msg, err: readErr}
			if readErr != nil {
				return
			}
		}
	}()
	var commandID int64
	o := newObserver(sink, policy, after, func(method string, params any) (int64, error) {
		commandID++
		return commandID, sendReadOnly(ctx, conn, commandID, method, params)
	})
	defer func() {
		cancel()
		for p := range packets {
			if p.err == nil {
				if e := o.handle(&p.message, false); e != nil {
					err = errors.Join(err, e)
				}
			}
		}
		wg.Wait()
		err = errors.Join(err, <-closeResult)
		err = errors.Join(err, o.expire(time.Now(), true))
		stop := Event{Kind: "capture_stop"}
		if err != nil {
			stop.Error = err.Error()
		}
		err = errors.Join(err, o.emit(stop))
	}()
	if err := o.emit(Event{Kind: "capture_start"}); err != nil {
		return err
	}
	enableID, err := o.send(network.CommandEnable, network.Enable().WithMaxTotalBufferSize(policy.MaxTotalBytes).WithMaxResourceBufferSize(policy.MaxBodyBytes))
	if err != nil {
		return err
	}
	enabled := false
	enableDeadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if !enabled && !now.Before(enableDeadline) {
				return fmt.Errorf("CDP Network.enable timed out")
			}
			if err := o.expire(now, false); err != nil {
				return err
			}
		case p, ok := <-packets:
			if !ok {
				return fmt.Errorf("CDP reader ended")
			}
			if p.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if errors.Is(p.err, io.EOF) {
					return fmt.Errorf("CDP target disconnected")
				}
				return fmt.Errorf("read CDP observation stream: %w", p.err)
			}
			if p.message.ID == enableID {
				if p.message.Error != nil {
					return fmt.Errorf("CDP Network.enable failed (code %d)", p.message.Error.Code)
				}
				enabled = true
				if ready != nil {
					ready()
				}
			}
			if err := o.handle(&p.message, true); err != nil {
				return err
			}
		}
	}
}
