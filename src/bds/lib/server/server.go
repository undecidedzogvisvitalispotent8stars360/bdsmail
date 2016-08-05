package server

import (
	"bytes"
	"errors"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"bds/lib/db"
	"bds/lib/lua"
	"bds/lib/maildir"
	"bds/lib/sendmail"
	"bds/lib/smtp"
	"bds/lib/i2p"
	"net"
	"net/http"
	"strings"
	"sync"
)
// handler of mail messages
type MailHandler interface {
	// we got a mail message
	// handle it somehow
	GotMail(ev *MailEvent)
	// do we accept mail going to this recipiant?
	AllowRecipiant(recip string) bool
}

// mail server
type Server struct {

	// custom mail handler
	Handler MailHandler

	// unexported fields

	// lua interpreter core
	l *lua.Lua

	inserv *smtp.Server
	outserv *smtp.Server
	// listener for smtp recv server
	maillistener net.Listener
	// listener for smtp send server
	smtplistener net.Listener
	// stream session with i2p router
	session i2p.StreamSession
	// listener for web server
	weblistener net.Listener
	// recv mail events from handlers
	chnl chan *MailEvent
	// maildir storage
	mail maildir.MailDir
	// lock to use to ensure 1 thread accessing lua
	luamtx sync.RWMutex
	// filepath to configuration
	configFname string
	// database access object
	dao db.DB
	// web ui
	webHandler http.Handler
	// mail sender
	mailer *sendmail.Mailer
}

func (s *Server) Bind() (err error) {
	// we touch the lua config so lock
	s.luamtx.Lock()
	defer s.luamtx.Unlock()
	
	// keyfile for i2p destination
	keyfile, ok := s.l.GetConfigOpt("i2pkeyfile")
	if !ok {
		keyfile = "bdsmail-privkey.dat"
	}
	// address of i2p router
	i2paddr, ok := s.l.GetConfigOpt("i2paddr")
	if !ok {
		i2paddr = "127.0.0.1:7656"
	}
	log.Info("Starting up I2P connection... hang tight we'll get there")
	// craete session
	session, err := i2p.NewSessionEasy(i2paddr, keyfile)
	if err == nil {
		// made session

		// get local smtp address
		addr, ok := s.l.GetConfigOpt("bindmail")
		if !ok {
			addr = "127.0.0.1:2525"
		}
		// bind smtp server
		s.smtplistener, err = net.Listen("tcp", addr)
		if err == nil {
			// success
			s.maillistener = session
			s.session = session
			log.Infof("We are %s", session.B32())
			s.mailer = sendmail.NewMailer()
			s.mailer.Retries = 10
			s.mailer.Dial = session.Dial
			s.mailer.Resolve = func(addr string) (net.Addr, error) {
				return session.LookupI2P(addr)
			}
		} else {
			// close session we got an error setting up local smtp listener
			session.Close()
		}
	}
	return
}

// dial out
func (s *Server) dial(net, addr string) (c net.Conn, err error) {
	if strings.HasSuffix(addr, ".i2p") {
		c, err = s.session.Dial(net, addr)
	} else {
		// TODO: over tor?
		// c, err = net.Dial(net, addr)
		err = errors.New("cannot dial outside of i2p")
	}
	return 
}

// queue mail to be filtered
func (s *Server) queueMail(addr net.Addr, from string, to []string, body []byte) {
	// for each recip fire a mail event
	for _, recip := range to {
		ev := &MailEvent{
			Addr:   addr,
			Sender: from,
			Recip:  recip,
			Body:   bytes.NewBuffer(body),
		}
		s.chnl <- ev
	}
}

// get the maildir for a recipiant
func (s *Server) getUserMaildir(recip string) (d maildir.MailDir) {
	// TODO: implement
	d = s.mail
	return
}

// we got mail that was not dropped by the filters
func (s *Server) gotMail(ev *MailEvent) (err error) {
	log.Info("we got mail for ", ev.Recip, " from ", ev.Sender)
	// deliver to maildir if set
	if s.mail.String() != "" {
		r := bytes.NewReader(ev.Body.Bytes())
		// deliver
		err = s.mail.Deliver(r)
	}
	if s.Handler != nil {
		s.Handler.GotMail(ev)
	}
	return
}

// run a lua filter given a mail event
// return the code returned by the lua function
func (s *Server) runFilter(filtername string, ev *MailEvent) int {
	// acquire lua lock
	s.luamtx.Lock()
	defer s.luamtx.Unlock()
	log.Debug(`running filter "` + filtername + `"...`)
	ret := s.l.CallMailFilter(filtername, ev.Addr.String(), ev.Recip, ev.Sender, ev.Body.String())
	log.Debug(`filter "`+filtername+`" returned `, ret)
	return ret
}

// check that a remote address is valid for the recipiant
// this can block for a bit
func (s *Server) i2pSenderIsValid(addr net.Addr, from string) (valid bool) {
	fromAddr := parseFromI2PAddr(from)
	if len(fromAddr) > 0 {
		tries := 16
		for tries > 0 {
			log.Info("looking up recipiant address %s", fromAddr)
			raddr, err := s.session.LookupI2P(fromAddr)
			if err == nil {
				// lookup worked
				valid = raddr.String() == addr.String()
				break
			} else {
				log.Warn("could not lookup %s", fromAddr)
				tries --
			}
		}
	}
	return
}

// called for each recipiant
// checks mail message against whitelist, blacklist and
// checkspam filters sequentially
func (s *Server) filterMail(ev *MailEvent) (err error) {
	// check invalid address for i2p
	if ! s.i2pSenderIsValid(ev.Addr, ev.Sender) {
		// bad address
		return
	}
	
	// check whitelist filter
	if s.runFilter("whitelist", ev) == 1 {
		// explicit whitelist
		err = s.gotMail(ev)
		return
	}
	// check blacklist filter
	if s.runFilter("blacklist", ev) == 1 {
		// drop message
		log.WithFields(log.Fields{
			"addr":    ev.Addr,
			"recip":   ev.Recip,
			"sender":  ev.Sender,
			"msgsize": ev.Body.Len(),
		}).Info("message hit blacklist")
		return
	}
	if s.runFilter("checkspam", ev) == 1 {
		// we got a spam message
		log.WithFields(log.Fields{
			"addr":    ev.Addr,
			"recip":   ev.Recip,
			"sender":  ev.Sender,
			"msgsize": ev.Body.Len(),
		}).Info("message hit spam filter")
		return
	}
	// this mail was accepted
	err = s.gotMail(ev)
	return
}

func (s *Server) Run() {
	// run recv mail acceptor
	go func() {
		log.Info("Serving Inbound SMTP server")
		err := s.inserv.Serve(s.maillistener)
		log.Info("SMTP Server ended")
		if err != nil {
			log.Fatal("inbound smtp died ", err)
		}
	}()

	// run send mail acceptor
	go func() {
		log.Info("Server Outbound SMTP Server on ", s.smtplistener.Addr())
		err := s.outserv.Serve(s.smtplistener)
		if err != nil {
			log.Fatal("outbound smtp died ", err)
		}
	}()
	
	log.Debug("run mail")
	for {
		// filtering
		ev, ok := <-s.chnl
		if !ok {
			log.Info("exiting mainloop")
			return
		}
		recip := ev.Recip
		if s.allowRecip(recip) {
			err := s.filterMail(ev)
			if err != nil {
				log.Error("Error while handling inbound mail ", err)
			}
		} else {
			log.Info("Ingoring message with invalid recipiant ", recip)
		}
	}
}

// do we allow a recipiant ?
func (s *Server) allowRecip(recip string) (allow bool) {
	if s.Handler == nil {
		// allow recip that only match the hostname of the server or the base32 address of the server
		addr := parseFromI2PAddr(recip)
		allow = addr == s.session.B32() || addr == s.inserv.Hostname
	} else {
		// custom mail handler
		allow = s.Handler.AllowRecipiant(recip)
	}
	return
}

// handle mail for sending from inet to i2p
func (s *Server) handleInetMail(remote net.Addr, from string, to []string, body []byte) {
	log.Debugf("handle send mail from %s", remote)
	us := parseFromI2PAddr(from)
	if us == s.inserv.Hostname || us == s.session.B32() {

		var jobs []*sendmail.DeliverJob
		// channel to connect channels to close
		chnl := make(chan chan bool)
		
		// deliver to all
		for _, recip := range to {
			if ! strings.HasSuffix(recip, ".i2p") {
				log.Warnf("Not delivering %s as it's not inside i2p", recip)
				continue
			}
			// fire off delivery job
			d := s.mailer.Deliver(recip, from, body)
			jobs = append(jobs, d)
			// collect job
			go func (j *sendmail.DeliverJob) {
				if <- j.Result {
					// successful delivery
				}
				chnl <- j.Result
			}(d)
		}
		// collect delivery jobs
		l := len(jobs)
		for l > 0 {
			c := <- chnl
			close(c)
			l --
		}
		
	} else {
		log.Errorf("bad outbound mail from %s", us)
	}
}

// stop server
func (s *Server) Stop() {
	close(s.chnl)
	s.luamtx.Lock()
	if s.mailer != nil {
		s.mailer.Quit()
		s.mailer = nil
	}
	if s.maillistener != nil {
		s.maillistener.Close()
		s.maillistener = nil
	}
	if s.smtplistener != nil {
		s.smtplistener.Close()
		s.smtplistener = nil
	}
	if s.weblistener != nil {
		s.weblistener.Close()
		s.weblistener = nil
	}
	if s.l != nil {
		s.l.Close()
		s.l = nil
	}
	s.luamtx.Unlock()
	log.Info("Server Stopped")
}

// load configuration file
func (s *Server) LoadConfig(fname string) (err error) {
	log.Debug("Load config file ", fname)
	s.configFname = fname
	err = s.ReloadConfig()
	return
}

// reload server configuration
func (s *Server) ReloadConfig() (err error) {
	// acquire lua lock
	s.luamtx.Lock()
	defer s.luamtx.Unlock()
	err = s.l.LoadFile(s.configFname)
	if err != nil {
		return
	}
	str, _ := s.l.GetConfigOpt("maildir")
	if len(str) > 0 {
		log.Info("Using maildir at ", str)
		s.mail = maildir.MailDir(str)
		err = s.mail.Ensure()
		if err != nil {
			return
		}
	}
	str, _ = s.l.GetConfigOpt("domain")
	if len(str) == 0 {
		if s.session != nil {
			str = s.session.B32()
		} else {
			str = "localhost"
		}
	}
	log.Info("Setting mail hostname to ", str)
	s.inserv.Hostname = str
	s.outserv.Hostname = "bdsmail"
	// only initialize dao if not initialized
	if s.dao == nil {
		str, _ = s.l.GetConfigOpt("database")
		if len(str) == 0 {
			str = "mailserver.sqlite"
		}
		log.Info("Initialize database ", str)
		var dao db.DB
		dao, err = db.NewDB(str)
		if err == nil {		
			err = dao.Ensure()
			if err == nil {
				log.Info("Database ready")
				s.dao = dao
			}
		}
	}
	return
}

func New() (s *Server) {
	Appname := fmt.Sprintf("BDSMail-%s", Version())
	s = &Server{
		chnl: make(chan *MailEvent, 10),
		l:    lua.New(),
		inserv: &smtp.Server{
			Appname: Appname,
		},
		outserv: &smtp.Server{
			Appname: Appname,
		},
	}
	if s.l.JIT() != nil {
		log.Fatal("failed to initialize luajit")
	}
	s.inserv.Handler = s.queueMail
	s.outserv.Handler = s.handleInetMail
	return
}
