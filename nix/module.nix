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
        systemd EnvironmentFile holding the instance settings:
        `MEDEWID_MACHINE_ADDR`, `MEDEWID_XML_PATH` and the TLS paths
        `MEDEWID_TLS_CERT`, `MEDEWID_TLS_KEY`, `MEDEWID_TLS_CLIENT_CA`.
        Kept out of the Nix store because it names private paths. The
        service never passes `-insecure-http`.
      '';
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
        ];
        EnvironmentFile = cfg.environmentFile;
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
