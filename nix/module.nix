# NixOS module for medewid. Imported as `nixosModules.medewid` by the flake,
# which supplies `self` so the package default can point at this flake.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.medewid;
  xmlName = baseNameOf cfg.xmlFile;
  # ":8443", "0.0.0.0:8443" and "[::]:8443" all end in the port.
  listenPort = lib.toInt (lib.last (lib.splitString ":" cfg.listen));
  # An absolute path string outside the store: a path literal (./medewid.key)
  # would copy the key or the vendor XML into the world-readable store.
  secretPath = lib.types.pathWith {
    inStore = false;
    absolute = true;
  };
  # Quote one ExecStart word for systemd, keeping `%d` (the credentials
  # directory specifier) live while neutralising other specifiers.
  credArg =
    name:
    "\"%d/${lib.replaceStrings [ "%" "$" "\"" ] [ "%%" "$$" "\\\"" ] name}\"";
in
{
  options.services.medewid = {
    enable = lib.mkEnableOption "medewid, the leva! espresso-machine daemon";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.medewid;
      defaultText = lib.literalExpression "medewi.packages.\${system}.medewid";
      description = "The medewid package to run.";
    };

    environmentFile = lib.mkOption {
      type = lib.types.path;
      example = "/run/secrets/medewid.env";
      description = ''
        systemd EnvironmentFile with the remaining instance settings:
        `MEDEWID_MACHINE_ADDR` (required) and optionally
        `MEDEWID_EVENT_EXEC`, `MEDEWID_RAW_*` and `MEDEWID_DISPLAY_*` (see
        the README). A `MEDEWID_DB` override must stay under
        `/var/lib/medewid` (the only writable path), as must
        `MEDEWID_RAW_DIR`. The XML and TLS files
        are not read from here; see `xmlFile` and the `tls*` options. The
        service never passes `-insecure-http`.
      '';
    };

    xmlFile = lib.mkOption {
      type = secretPath;
      example = "/run/secrets/leva!-65FCB201.xml";
      description = ''
        The firmware's vendor Status-Monitor XML. Delivered via systemd
        credentials, so it may be root-owned 0600 and live anywhere
        (including /home or /run/secrets). Its base name (without
        extension) must match the machine's `XML=` reply, so it is kept
        as the credential name.
      '';
    };

    tlsCert = lib.mkOption {
      type = secretPath;
      example = "/run/secrets/medewid.crt";
      description = "Server certificate PEM, delivered via systemd credentials.";
    };

    tlsKey = lib.mkOption {
      type = secretPath;
      example = "/run/secrets/medewid.key";
      description = "Server key PEM, delivered via systemd credentials (may be root-owned 0600).";
    };

    tlsClientCA = lib.mkOption {
      type = secretPath;
      example = "/run/secrets/clients-ca.crt";
      description = "CA PEM that client certificates must chain to, delivered via systemd credentials.";
    };

    listen = lib.mkOption {
      type = lib.types.str;
      default = ":8443";
      description = "Address of the mTLS API listener.";
    };

    healthAddr = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:9114";
      description = "Address of the plaintext /healthz listener.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open the `listen` port in the firewall.";
    };

    ipAddressAllow = lib.mkOption {
      type = lib.types.nullOr (lib.types.listOf lib.types.str);
      default = null;
      example = [
        "192.168.178.0/24"
        "10.100.0.0/24"
      ];
      description = ''
        Networks the daemon may exchange packets with, besides loopback
        (systemd `IPAddressAllow`; everything else is denied). Must cover the
        machine, the API clients (Prometheus, phones, `medewictl`), the DNS
        server if `MEDEWID_MACHINE_ADDR` is a name, and any host the event
        hook talks to. A compromised daemon then cannot reach the rest of
        the network. `null` (the default) applies no IP filter.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.medewid = {
      description = "medewid leva! espresso-machine daemon";
      wantedBy = [ "multi-user.target" ];
      after = [
        "network-online.target"
        "time-sync.target"
      ];
      wants = [ "network-online.target" ];
      serviceConfig = {
        ExecStart = lib.concatStringsSep " " [
          "${cfg.package}/bin/medewid"
          "-listen ${lib.escapeShellArg cfg.listen}"
          "-health-addr ${lib.escapeShellArg cfg.healthAddr}"
          "-xml ${credArg xmlName}"
          "-tls-cert ${credArg "tls-cert"}"
          "-tls-key ${credArg "tls-key"}"
          "-tls-client-ca ${credArg "tls-client-ca"}"
        ];
        EnvironmentFile = cfg.environmentFile;
        LoadCredential = [
          "${xmlName}:${cfg.xmlFile}"
          "tls-cert:${cfg.tlsCert}"
          "tls-key:${cfg.tlsKey}"
          "tls-client-ca:${cfg.tlsClientCA}"
        ];
        DynamicUser = true;
        StateDirectory = "medewid";
        Restart = "always";
        RestartSec = 5;
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        # Sandbox: a bug in medewid must not become a foothold on the host.
        # The event hook runs inside the same sandbox.
        UMask = "0077";
        CapabilityBoundingSet = "";
        PrivateDevices = true;
        PrivateUsers = true;
        ProtectClock = true;
        ProtectControlGroups = true;
        ProtectHostname = true;
        ProtectKernelLogs = true;
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectProc = "invisible";
        ProcSubset = "pid";
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
        ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [
          "@system-service"
          "~@privileged"
        ];
        SystemCallErrorNumber = "EPERM";
      }
      // lib.optionalAttrs (cfg.ipAddressAllow != null) {
        IPAddressDeny = "any";
        IPAddressAllow = [ "localhost" ] ++ cfg.ipAddressAllow;
      };
    };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ listenPort ];
  };
}
