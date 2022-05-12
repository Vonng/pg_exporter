FROM scratch
LABEL maintainer="Vonng <rh@vonng.com>"

COPY pg_exporter /bin/pg_exporter
COPY pg_exporter.yml /etc/pg_exporter.yml

ENTRYPOINT ["/bin/pg_exporter"]