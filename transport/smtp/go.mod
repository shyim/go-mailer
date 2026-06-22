module github.com/shyim/go-mailer/transport/smtp

go 1.26

require (
	github.com/shyim/go-mailer v0.0.0-00010101000000-000000000000
	github.com/shyim/go-mailer/transport/sendmail v0.0.0-00010101000000-000000000000
	golang.org/x/net v0.55.0
)

require golang.org/x/text v0.38.0 // indirect

replace github.com/shyim/go-mailer => ../..

replace github.com/shyim/go-mailer/transport/sendmail => ../sendmail
