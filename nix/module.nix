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
        `MEDEWID_EVENT_EXEC`. A `MEDEWID_DB` override must stay under
        `/var/lib/medewid` (the only writable path). The XML and TLS files
        are not read from here; see `xmlFile` and the `tls*` options. The
        service never passes `-insecure-http`.
      '';
    };

    xmlFile = lib.mkOption {
      type = lib.types.path;
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
      type = lib.types.path;
      example = "/run/secrets/medewid.crt";
      description = "Server certificate PEM, delivered via systemd credentials.";
    };

    tlsKey = lib.mkOption {
      type = lib.types.path;
      example = "/run/secrets/medewid.key";
      description = "Server key PEM, delivered via systemd credentials (may be root-owned 0600).";
    };

    tlsClientCA = lib.mkOption {
      type = lib.types.path;
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
  };

  config = lib.mkIf cfg.enable {
    systemd.services.medewid = {
      description = "medewid leva! espresso-machine daemon";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
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
      };
    };
  };
}
