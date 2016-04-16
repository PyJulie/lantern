package detour

import (
	"fmt"
	"net"
	"sync/atomic"
)

type directConn struct {
	net.Conn
	network      string
	addr         string
	readFirst    int32
	shouldDetour int32
}

func detector() *Detector {
	return blockDetector.Load().(*Detector)
}

func newDirectConn(network, addr string) *directConn {
	return &directConn{
		Conn: newEventualConn(
			DialTimeout,
		),
		network:      network,
		addr:         addr,
		shouldDetour: 1, // 1 means true, will change to 0 once connected
	}
}

func (dc *directConn) Dial() (ch chan error) {
	return dc.Conn.(*eventualConn).Dial(func() (net.Conn, error) {
		conn, err := net.DialTimeout(dc.network, dc.addr, DialTimeout)
		if err == nil {
			if detector().DNSPoisoned(conn) {
				if err := conn.Close(); err != nil {
					log.Debugf("Error closing direct connection to %s: %s", dc.addr, err)
				}
				log.Debugf("Dial directly to %s, dns hijacked", dc.addr)
				return nil, fmt.Errorf("DNS hijacked")
			}
			log.Tracef("Dial directly to %s succeeded", dc.addr)
			return conn, nil
		} else if detector().TamperingSuspected(err) {
			log.Debugf("Dial directly to %s, tampering suspected: %s", dc.addr, err)
		} else {
			log.Debugf("Dial directly to %s failed: %s", dc.addr, err)
		}
		return nil, err
	})
}

func (dc *directConn) Read(b []byte) chan ioResult {
	log.Tracef("Reading from directConn to %s", dc.addr)
	checker := dc.checkFollowupRead
	if atomic.CompareAndSwapInt32(&dc.readFirst, 0, 1) {
		checker = dc.checkFirstRead
	}
	ch := make(chan ioResult, 1)
	go func() {
		result := ioResult{}
		dc.setShouldDetour(true)
		result.i, result.err = dc.doRead(b, checker)
		log.Tracef("Read %d bytes from directConn to %s, err: %v", result.i, dc.addr, result.err)
		if result.err == nil {
			dc.setShouldDetour(false)
		}
		ch <- result
	}()
	return ch
}

func (dc *directConn) Write(b []byte) chan ioResult {
	ch := make(chan ioResult, 1)
	go func() {
		result := ioResult{}
		result.i, result.err = dc.Conn.Write(b)
		ch <- result
	}()
	return ch
}

type readChecker func([]byte, error) error

func (dc *directConn) checkFirstRead(b []byte, err error) error {
	if err != nil {
		log.Debugf("Error while read from %s directly (first): %s", dc.addr, err)
		if detector().TamperingSuspected(err) {
			dc.setShouldDetour(true)
		}
		return err
	}
	if detector().FakeResponse(b) {
		log.Debugf("Read %d bytes from %s directly, response is hijacked", len(b), dc.addr)
		dc.setShouldDetour(true)
		return fmt.Errorf("response is hijacked")
	}
	log.Tracef("Read %d bytes from %s directly (first)", len(b), dc.addr)
	return nil
}

func (dc *directConn) checkFollowupRead(b []byte, err error) error {
	if err != nil {
		log.Debugf("Error while read from %s directly (follow-up): %s", dc.addr, err)
		if detector().TamperingSuspected(err) {
			log.Debugf("Seems %s is still blocked, should detour next time", dc.addr)
			dc.setShouldDetour(true)
		}
		return err
	}
	if detector().FakeResponse(b) {
		log.Tracef("%s is still content hijacked, should detour next time", dc.addr)
		dc.setShouldDetour(true)
		return fmt.Errorf("response is hijacked")
	}
	log.Tracef("Read %d bytes from %s directly (follow-up)", len(b), dc.addr)
	return nil
}

func (dc *directConn) doRead(b []byte, checker readChecker) (int, error) {
	n, err := dc.Conn.Read(b)
	err = checker(b[:n], err)
	if err != nil {
		n = 0
	}
	return n, err
}

func (dc *directConn) Close() (err error) {
	err = dc.Conn.Close()
	return
}

func (dc *directConn) setShouldDetour(should bool) {
	log.Tracef("should detour to %s? %v", dc.addr, should)
	if should {
		atomic.StoreInt32(&dc.shouldDetour, 1)
	} else {
		atomic.StoreInt32(&dc.shouldDetour, 0)
	}

}

func (dc *directConn) ShouldDetour() bool {
	return atomic.LoadInt32(&dc.shouldDetour) == 1
}
