package shadowsocks

import (
	"bufio"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/ccsexyz/utils"
	"github.com/golang/snappy"
)

type Conn utils.Conn

type sconn struct {
	net.Conn
	r *bufio.Reader
}

func Newsconn(c net.Conn) *sconn {
	return &sconn{Conn: c, r: bufio.NewReader(c)}
}

func (c *sconn) WriteBuffers(b [][]byte) (n int, err error) {
	buffers := net.Buffers(b)
	var n2 int64
	n2, err = buffers.WriteTo(c.Conn)
	n = int(n2)
	return
}

func (c *sconn) Read(b []byte) (n int, err error) {
	n, err = c.r.Read(b)
	return
}

var (
	xuch  chan net.Conn
	xudie chan bool
)

func init() {
	xuch = make(chan net.Conn, 32)
	xudie = make(chan bool)
	go xuroutine()
}

type DebugConn struct {
	net.Conn
	c *Config
}

func NewDebugConn(conn net.Conn, c *Config) *DebugConn {
	return &DebugConn{Conn: conn, c: c}
}

func (c *DebugConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err == nil && n > 0 {
		c.c.LogD("read", n, "bytes from", c.RemoteAddr(), b[:n])
	}
	return
}

func (c *DebugConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if err == nil && n > 0 {
		c.c.LogD("write", n, "bytes to", c.RemoteAddr(), "from", c.LocalAddr(), b[:n])
	}
	return
}

func debugAcceptHandler(conn net.Conn, lis *listener) (c net.Conn) {
	if lis.c.Debug {
		c = &DebugConn{
			Conn: conn,
			c:    lis.c,
		}
	} else {
		c = conn
	}
	return
}

type SsConn struct {
	Conn
	enc        utils.Encrypter
	dec        utils.Decrypter
	c          *Config
	xu1s       bool
	reqenc     bool
	encnum     int
	reqdec     bool
	decnum     int
	partenc    bool
	partencnum int
}

func (c *SsConn) GetConfig() *Config {
	return c.c
}

func (c *SsConn) Close() error {
	if c.xu1s {
		c.xu1s = false
		select {
		case <-xudie:
		case xuch <- c:
			return nil
		}
	}
	return c.Conn.Close()
}

func NewSsConn(conn Conn, c *Config) *SsConn {
	return &SsConn{
		Conn:   conn,
		c:      c,
		reqdec: true,
		reqenc: true,
	}
}

func (c *SsConn) Xu1s() {
	c.xu1s = true
}

func (c *SsConn) Xu0s() {
	c.xu1s = false
}

func (c *SsConn) Read(b []byte) (n int, err error) {
	if c.partenc && !c.reqdec {
		c.dec = nil
		return c.Conn.Read(b)
	}
	if c.dec == nil {
		iv := make([]byte, c.c.Ivlen)
		_, err = io.ReadFull(c.Conn, iv)
		if err != nil {
			return
		}
		c.dec, err = utils.NewDecrypter(c.c.Method, c.c.Password, iv)
		if err != nil {
			return
		}
	}
	n, err = c.Conn.Read(b)
	if n <= 0 {
		return
	}
	if !c.partenc {
		c.dec.Decrypt(b[:n], b[:n])
		return
	}
	if c.decnum+n >= c.partencnum {
		m := c.partencnum - c.decnum
		c.dec.Decrypt(b[:m], b[:m])
		c.reqdec = false
	} else {
		c.decnum += n
		c.dec.Decrypt(b[:n], b[:n])
	}
	return
}

func (c *SsConn) initEncrypter() (err error) {
	if c.enc == nil {
		c.enc, err = utils.NewEncrypter(c.c.Method, c.c.Password)
	}
	return
}

func (c *SsConn) Write(b []byte) (n int, err error) {
	if c.partenc && !c.reqenc {
		c.enc = nil
		return c.Conn.Write(b)
	}
	bufs := make([][]byte, 0, 2)
	if c.enc == nil {
		c.enc, err = utils.NewEncrypter(c.c.Method, c.c.Password)
		if err != nil {
			return
		}
		bufs = append(bufs, c.enc.GetIV())
	}
	if !c.partenc {
		c.enc.Encrypt(b, b)
	} else {
		if c.reqenc {
			if c.encnum+len(b) >= c.partencnum {
				m := c.partencnum - c.encnum
				c.enc.Encrypt(b[:m], b[:m])
				c.reqenc = false
			} else {
				c.encnum += len(b)
				c.enc.Encrypt(b, b)
			}
		}
	}
	bufs = append(bufs, b)
	_, err = c.Conn.WriteBuffers(bufs)
	if err == nil {
		n = len(b)
	}
	return
}

func (c *SsConn) WriteBuffers(b [][]byte) (n int, err error) {
	if c.partenc && !c.reqenc {
		c.enc = nil
		return c.Conn.WriteBuffers(b)
	}
	bufs := make([][]byte, 0, len(b)+1)
	if c.enc == nil {
		c.enc, err = utils.NewEncrypter(c.c.Method, c.c.Password)
		if err != nil {
			return
		}
		bufs = append(bufs, c.enc.GetIV())
	}
	for it := 0; it < len(b); it++ {
		buf := b[it]
		if !c.partenc {
			c.enc.Encrypt(buf, buf)
		} else {
			if c.reqenc {
				if c.encnum+len(buf) >= c.partencnum {
					m := c.partencnum - c.encnum
					c.enc.Encrypt(buf[:m], buf[:m])
					c.reqenc = false
				} else {
					c.encnum += len(buf)
					c.enc.Encrypt(buf, buf)
				}
			}
		}
		bufs = append(bufs, buf)
		n += len(b[it])
	}
	_, err = c.Conn.WriteBuffers(bufs)
	if err != nil {
		n = 0
	}
	return
}

func xuroutine() {
	ticker := time.NewTicker(time.Second)
	mn := make(map[string]int)
	mc := make(map[string]net.Conn)
	defer func() {
		for _, v := range mc {
			v.Close()
		}
	}()
	for {
		select {
		case <-xudie:
			return
		case c := <-xuch:
			s := c.RemoteAddr().String()
			a := 0
			b := 0
			for b < 3 || b > 14 {
				b = rand.Intn(16) + 1
				a += b
			}
			if _, ok := c.(*SsConn); ok {
				c.(*SsConn).c.Log("xu ", a, "seconds")
			}
			mn[s] = a
			mc[s] = c
		case <-ticker.C:
			for k, v := range mn {
				v--
				if v <= 0 {
					delete(mn, k)
					c, ok := mc[k]
					if ok {
						delete(mc, k)
						if _, ok := c.(*SsConn); ok {
							c.(*SsConn).xu1s = false
						}
						c.Close()
					}
				} else {
					mn[k] = v
				}
			}
		}
	}
}

type DstConn struct {
	Conn
	dst Addr
}

func NewDstConn(conn net.Conn, dst Addr) *DstConn {
	return &DstConn{Conn: GetConn(conn), dst: dst}
}

func (c *DstConn) GetDst() string {
	return net.JoinHostPort(c.dst.Host(), c.dst.Port())
}

type LimitConn struct {
	Conn
	Rlimiters []*Limiter
	Wlimiters []*Limiter
}

func (c *LimitConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err == nil {
		for _, v := range c.Rlimiters {
			v.Update(n)
		}
	}
	return
}

func (c *LimitConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if err == nil {
		for _, v := range c.Wlimiters {
			v.Update(n)
		}
	}
	return
}

func (c *LimitConn) WriteBuffers(b [][]byte) (n int, err error) {
	n, err = c.Conn.WriteBuffers(b)
	if err == nil {
		for _, v := range c.Wlimiters {
			v.Update(n)
		}
	}
	return
}

func limitAcceptHandler(conn Conn, lis *listener) (c Conn) {
	limiters := make([]*Limiter, len(lis.c.limiters))
	copy(limiters, lis.c.limiters)
	if lis.c.LimitPerConn != 0 {
		limiters = append(limiters, NewLimiter(lis.c.LimitPerConn))
	}
	c = &LimitConn{
		Conn:      GetConn(conn),
		Rlimiters: limiters,
	}
	return
}

type MuxConn struct {
	conn Conn
	Conn
}

type HttpLogConn struct {
	net.Conn
	pr *utils.HTTPHeaderParser
	pw *utils.HTTPHeaderParser
	c  *Config
}

func NewHttpLogConn(conn net.Conn, c *Config) *HttpLogConn {
	return &HttpLogConn{
		Conn: conn,
		pr:   utils.NewHTTPHeaderParser(bufPool.Get().([]byte)),
		pw:   utils.NewHTTPHeaderParser(bufPool.Get().([]byte)),
		c:    c,
	}
}

func cleanHTTPParser(p *utils.HTTPHeaderParser) {
	if p != nil {
		bufPool.Put(p.GetBuf())
	}
}

func (conn *HttpLogConn) Close() error {
	if conn.pr != nil {
		cleanHTTPParser(conn.pr)
		conn.pr = nil
	}
	if conn.pw != nil {
		cleanHTTPParser(conn.pw)
		conn.pw = nil
	}
	return conn.Conn.Close()
}

func (conn *HttpLogConn) Read(b []byte) (n int, err error) {
	n, err = conn.Conn.Read(b)
	if conn.pr != nil {
		ok, e := conn.pr.Read(b[:n])
		if ok {
			var n2 int
			buf := bufPool.Get().([]byte)
			defer bufPool.Put(buf)
			n2, e = conn.pr.Encode(buf)
			if err == nil {
				conn.c.Log(conn.LocalAddr(), "->", conn.RemoteAddr(), utils.SliceToString(buf[:n2]))
			}
		}
		if e != nil || ok {
			cleanHTTPParser(conn.pr)
			conn.pr = nil
		}

	}
	return
}

func (conn *HttpLogConn) Write(b []byte) (n int, err error) {
	if conn.pw != nil {
		ok, e := conn.pw.Read(b)
		if ok {
			var n2 int
			buf := bufPool.Get().([]byte)
			defer bufPool.Put(buf)
			n2, e = conn.pw.Encode(buf)
			if err == nil {
				conn.c.Log(conn.LocalAddr(), "->", conn.RemoteAddr(), utils.SliceToString(buf[:n2]))
			}
		}
		if e != nil || ok {
			cleanHTTPParser(conn.pw)
			conn.pw = nil
		}

	}
	return conn.Conn.Write(b)
}

type SnappyConn struct {
	net.Conn
	w *snappy.Writer
	r *snappy.Reader
}

func NewSnappyConn(conn net.Conn) *SnappyConn {
	return &SnappyConn{
		Conn: conn,
		w:    snappy.NewBufferedWriter(conn),
		r:    snappy.NewReader(conn),
	}
}

func (c *SnappyConn) Read(b []byte) (n int, err error) {
	return c.r.Read(b)
}

func (c *SnappyConn) Write(b []byte) (n int, err error) {
	n, err = c.w.Write(b)
	if err == nil {
		err = c.w.Flush()
	}
	return n, err
}

func (c *SnappyConn) WriteBuffers(b [][]byte) (n int, err error) {
	var n2 int
	for _, v := range b {
		n2, err = c.Write(v)
		if err != nil {
			return
		}
		n += n2
	}
	return
}
