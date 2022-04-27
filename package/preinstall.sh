#!/bin/bash

# create a group & user named prometheus if not exists
getent group prometheus >/dev/null || groupadd -r prometheus ; /bin/true
getent passwd prometheus >/dev/null || useradd -r -g prometheus -s /sbin/nologin -c "Prometheus services" prometheus
exit 0