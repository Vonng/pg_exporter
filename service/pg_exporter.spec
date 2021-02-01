%define debug_package %{nil}

Name:           pg_exporter
Version:        0.3.2
Release:        1%{?dist}
Summary:        Prometheus exporter for PostgreSQL/Pgbouncer server metrics
License:        BSD
URL:            https://www.vonng.com/%{name}

Source0:        %{name}_v%{version}_linux-amd64.tar.gz
Source1:        %{name}.service
Source2:        %{name}.default
Source3:        %{name}.yaml

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
install -m 0644 %{name}.yaml %{buildroot}/%{_sysconfdir}/%{name}/%{name}.yaml
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
%config(noreplace) %{_sysconfdir}/%{name}/%{name}.yaml

%changelog
* Thu Feb 20 2020 Ruohang Feng <fengruohang@outlook.com> - 0.2.0-1
- add yum package and linux service definition
- add a 'skip' flag into query config
- fix `pgbouncer_up` metrics
- add conf reload support
