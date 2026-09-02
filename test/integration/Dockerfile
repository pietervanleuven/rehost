# A stand-in for a shared-hosting account: sshd, a database server on
# localhost, and the client tooling rehost probes for. Deliberately NOT a CMS
# image — a default WordPress install is a friendlier input than the fixtures
# in this directory, and the code under test here is the shell plumbing, not
# framework detection.
#
# One image serves every environment; entrypoint.sh removes tools and starts
# the engine the requested variant calls for, so switching variants costs a
# container start rather than an image build.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server \
      mariadb-server mariadb-client \
      postgresql postgresql-client \
      php-cli php-mysql php-pgsql \
      gzip tar findutils procps ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# The mysql-named symlinks are what a host "with mysqldump" looks like to the
# capability probe. Debian's MariaDB packaging has shipped them historically
# but is retiring them, so pin them explicitly: the mysql variant needs them
# present and the mariadb variant needs them gone, and neither should depend
# on what the distro happened to do this release.
RUN for pair in "mariadb mysql" "mariadb-dump mysqldump" "mariadb-admin mysqladmin"; do \
      set -- $pair; \
      if [ -x "/usr/bin/$1" ] && [ ! -e "/usr/bin/$2" ]; then ln -s "$1" "/usr/bin/$2"; fi; \
    done

RUN useradd --create-home --shell /bin/bash site \
    && mkdir -p /run/sshd /home/site/.ssh \
    && chown -R site:site /home/site/.ssh \
    && chmod 700 /home/site/.ssh

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY fixtures /opt/fixtures
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
