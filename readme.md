![logo](logo.png "logo")


# Brain Dead Simple Mail Server ᴮᴱᵀᴬ #

![MIT License](https://img.shields.io/github/license/majestrate/bdsmail.svg)
![Logo is ebin](https://img.shields.io/badge/logo-ebin-brightgreen.svg)

### Building From Source ###

Dependencies:

* go 1.9.4
* git
* make
* i2p with SAM enabled

To build the daemon do:

    $ git clone https://github.com/majestrate/bdsmail
    $ cd bdsmail
    $ make

### Configuring ###


To generate an initial configuration file run the following:

    $ ./bin/bdsconfig > config.ini
    
Make sure to set the admin password:

    $ ./bin/mailtool config.ini admin $PWD/mail/admin admin_password_goes_here

### Running ###

    $ ./bin/maild config.ini

### Email setup ###

See the example config for mutt [here](contrib/config/mutt/muttrc)

### Contact ###

test email address is `test@ivpxmoh2qzcmxbij3sxfnlsua6panhxke2b3bbhn4xxw7oacujdq.b32.i2p` 

if the server is down ding me on xmpp: `jeff@i2p.rocks`

## Features ##

### Current ###

* brain dead simple self hosted i2p mail
* brain dead simple database backend (sqlite3)
* brain dead simple smtp access
* brain dead simple pop3 access
* brain dead simple license (MIT)

### Near Future ###

* brain dead simple inet/i2p mail relay

### Future (Eventually) ###

* brain dead simple i2pbote gateway
