%define debug_package %{nil}

Name:           pg_exporter
Version:        0.4.0
Release:        1%{?dist}
Summary:        Prometheus exporter for PostgreSQL/Pgbouncer server metrics
License:        Apache-2.0 License
URL:            https://www.vonng.com/%{name}

Source0:        %{name}_v%{version}_linux-amd64.tar.gz
Source1:        %{name}.service
Source2:        %{name}.default
Source3:        %{name}.yml

%{?systemd_requires}
Requires(pre): shadow-utils

%description
Prometheus exporter for PostgreSQL / Pgbouncer server metrics.
Supported version: Postgres9.4+ & Pgbouncer 1.8+

%prep
%setup -q -n %{name}_v%{version}_linux-amd64

%build

%install
mkdir -p %{buildroot}/%{_bindir} %{buildroot}/%{_sysconfdir}/%{name} %{buildroot}/%{_sysconfdir}/default %{buildroot}%{_unitdir}
install -m 0755 %{name} %{buildroot}/%{_bindir}/%{name}
install -m 0644 %{name}.yml %{buildroot}/%{_sysconfdir}/%{name}/%{name}.yml
install -m 0644 %{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -m 0640 %{name}.default %{buildroot}/%{_sysconfdir}/default/%{name}

%pre
getent group prometheus >/dev/null || groupadd -r prometheus
getent passwd prometheus >/dev/null || \
  useradd -r -g prometheus -d %{_sharedstatedir}/prometheus -s /sbin/nologin \
          -c "Prometheus services" prometheus
exit 0

%post
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun %{name}.service

%files
%defattr(-,root,root,-)
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%config(noreplace) %{_sysconfdir}/default/%{name}
%config(noreplace) %{_sysconfdir}/%{name}/%{name}.yml

%changelog
* Thu May 27 2021 Ruohang Feng <rh@vonng.com> - 0.4.0-1
- Default metrics configuration overhaul
- Embed default metrics definition into binary
- add include-database and exclude-database option
- Add multiple database implementation
- Add reload command to rpm systemctl
