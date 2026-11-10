# NixOS VM test: the hardened medewid unit against levasim over mTLS.
# Proves the sandbox leaves the daemon working (machine link, API, health,
# event hook) and keeps the exposure score from creeping back up.
{ self, pkgs }:
let
  system = pkgs.stdenv.hostPlatform.system;

  # Throwaway test PKI: one CA signs the server and the admin client
  # certificate, a second CA the scrape-only one.
  pki = pkgs.runCommand "medewid-test-pki" { nativeBuildInputs = [ pkgs.openssl ]; } ''
    mkdir $out && cd $out
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
      -keyout ca.key -out ca.crt -days 3650 -subj /CN=medewid-test-ca
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
      -keyout metrics-ca.key -out metrics-ca.crt -days 3650 -subj /CN=medewid-test-metrics-ca
    sign() { # name, extensions, CA
      openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -keyout $1.key -out $1.csr -subj /CN=$1
      openssl x509 -req -in $1.csr -CA ''${3:-ca}.crt -CAkey ''${3:-ca}.key -CAcreateserial \
        -out $1.crt -days 3650 -extfile <(printf '%s\n' "$2")
    }
    sign server 'subjectAltName=DNS:brewhost
    extendedKeyUsage=serverAuth'
    sign client 'extendedKeyUsage=clientAuth'
    sign scraper 'extendedKeyUsage=clientAuth' metrics-ca
  '';

  # The synthetic stream plus the machine's "[READY]" notice, so the event
  # hook fires (MACHINE_READY) without waiting for a heat-up.
  fixtures = pkgs.runCommand "medewid-test-fixtures" { } ''
    mkdir $out
    cp ${../testdata/fixtures/synthetic}/* $out/
    chmod u+w $out/log.jsonl
    echo '{"t": 5500, "dir": "rx", "data": "'"$(printf '[READY]\r\n' | base64)"'"}' >> $out/log.jsonl
  '';
in
pkgs.testers.runNixOSTest {
  name = "medewid";
  nodes.brewhost = {
    imports = [ self.nixosModules.medewid ];
    networking.hosts."127.0.0.1" = [ "brewhost" ];
    environment.systemPackages = [ pkgs.curl ];

    # Secrets live outside the store in a real deployment; here they are
    # copied to /etc/medewi (root-owned 0600) to exercise LoadCredential.
    environment.etc = {
      "medewi/synthetic.xml" = {
        source = ../testdata/spec/synthetic.xml;
        mode = "0600";
      };
      "medewi/server.crt" = {
        source = "${pki}/server.crt";
        mode = "0600";
      };
      "medewi/server.key" = {
        source = "${pki}/server.key";
        mode = "0600";
      };
      "medewi/ca.crt" = {
        source = "${pki}/ca.crt";
        mode = "0600";
      };
      "medewi/metrics-ca.crt" = {
        source = "${pki}/metrics-ca.crt";
        mode = "0600";
      };
    };

    systemd.services.levasim = {
      wantedBy = [ "multi-user.target" ];
      before = [ "medewid.service" ];
      serviceConfig = {
        ExecStart = "${
          self.packages.${system}.levasim
        }/bin/levasim --addr 127.0.0.1:2300 --tripwire 127.0.0.1:2323 --fixtures ${fixtures}";
        DynamicUser = true;
      };
    };

    services.medewid = {
      enable = true;
      environmentFile = pkgs.writeText "medewid.env" ''
        MEDEWID_MACHINE_ADDR=127.0.0.1:2300
        MEDEWID_EVENT_EXEC=echo "$MEDEWI_EVENT" >> /var/lib/medewid/hook.log
      '';
      xmlFile = "/etc/medewi/synthetic.xml";
      tlsCert = "/etc/medewi/server.crt";
      tlsKey = "/etc/medewi/server.key";
      tlsClientCA = "/etc/medewi/ca.crt";
      tlsMetricsCA = "/etc/medewi/metrics-ca.crt";
      openFirewall = true;
      ipAddressAllow = [ "192.168.1.0/24" ];
    };
  };

  testScript = ''
    import json
    from datetime import timedelta

    brewhost.wait_for_unit("medewid.service")
    brewhost.wait_until_succeeds("journalctl -u medewid | grep -q 'reports started'", timeout=timedelta(seconds=60))
    brewhost.succeed("curl -sf http://127.0.0.1:9114/healthz")

    curl = "curl -sf --cacert ${pki}/ca.crt "
    mtls = curl + "--cert ${pki}/client.crt --key ${pki}/client.key "
    api = "https://brewhost:8443"
    status = json.loads(brewhost.succeed(mtls + api + "/api/status"))
    assert status["connected"] and status["spec_ok"], status
    brewhost.succeed(mtls + api + "/metrics | grep '^medewi_machine_connected 1' >/dev/null")
    brewhost.fail(curl + api + "/api/status")  # no client certificate

    # The scrape-only certificate reaches /metrics and nothing else.
    scraper = curl + "--cert ${pki}/scraper.crt --key ${pki}/scraper.key "
    brewhost.succeed(scraper + api + "/metrics | grep '^medewi_machine_connected 1' >/dev/null")
    code = brewhost.succeed("curl -s -o /dev/null -w '%{http_code}' --cacert ${pki}/ca.crt --cert ${pki}/scraper.crt --key ${pki}/scraper.key " + api + "/api/backup/mcu")
    assert code == "403", code

    # The tripwire never fired: medewid kept off the bootloader port.
    brewhost.fail("journalctl -u levasim | grep -q 'TRIPWIRE:'")

    # openFirewall opened the listen port.
    brewhost.succeed("iptables -S nixos-fw | grep -q 'dport 8443'")

    # The hook runs inside the sandbox.
    brewhost.wait_until_succeeds(
      "grep -qx MACHINE_READY /var/lib/private/medewid/hook.log", timeout=timedelta(seconds=60))

    score = float(brewhost.succeed(
      "systemd-analyze security medewid.service | tail -1 | grep -o '[0-9]*\\.[0-9]'"))
    print(f"exposure score {score}")
    assert score <= 1.5, f"exposure score {score} > 1.5"
  '';
}
