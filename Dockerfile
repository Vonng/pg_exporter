FROM scratch
ADD pg_exporter /
CMD ["/pg_exporter"]