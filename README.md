# domino2syslog

Runs an IBM Domino server and puts the log output in syslog where it belongs.

This replaces an earlier [Ruby-based version](https://gist.github.com/lpar/1092788).

In rsyslog, you can filter like this:

    :programname, isequal, "domino"    -/var/log/domino.log

To build, set GOOS and GOARCH to the architecture of your servers, and run
`go build -v`. Example:

    GOOS=linux GOARCH=amd64 go build -v

The resulting binary can then be copied straight to your server, with no need 
to install anything.

To incorporate into your startup scripts, just run the binary instead 
of `/opt/ibm/domino/bin/server`. By default it runs Domino for you in a
separate goroutine and processes the output.

