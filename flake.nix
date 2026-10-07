{
  description = "xivstream: stream FINAL FANTASY XIV with Sunshine, Wolf or Selkies, set up by a wizard";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        xivstream = pkgs.buildGoModule {
          pname = "xivstream";
          version = self.shortRev or "dev";
          src = ./.;
          # After changing go.mod/go.sum: set pkgs.lib.fakeHash, build, and paste the hash nix prints.
          vendorHash = "sha256-Kh9FwlPYK0l04KRamzYyNlUuur7/syRJebTfSxW2YxM=";
          subPackages = [ "cmd/xivstream" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=${self.shortRev or "dev"}" ];
          meta = {
            description = "Stream FINAL FANTASY XIV with Sunshine, Wolf or Selkies, set up by a wizard";
            homepage = "https://github.com/Spaceghost/xivstream-dalamud";
            license = pkgs.lib.licenses.mit;
            mainProgram = "xivstream";
          };
        };
        default = xivstream;
      });

      # NixOS declares services; `xivstream apply` does not write units there.
      # The wizard's config file is still the one source of settings.
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.xivstream;
          pkg = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
          toml = pkgs.formats.toml { };
          configFile = if cfg.settings != null then toml.generate "xivstream.toml" cfg.settings else cfg.configFile;
        in
        {
          options.services.xivstream = {
            enable = lib.mkEnableOption "xivstream's host services (GPU sharing, container start)";
            configFile = lib.mkOption {
              type = lib.types.path;
              default = "/etc/xivstream/config.toml";
              description = "Config written by `xivstream wizard` (used when settings is null).";
            };
            settings = lib.mkOption {
              type = lib.types.nullOr toml.type;
              default = null;
              example = { topology = "incus"; backend = "sunshine"; gpu_share = { mode = "reserve"; container = "almanac"; }; };
              description = "The config as Nix, instead of configFile.";
            };
          };
          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ pkg ];
            boot.kernelModules = [ "uinput" "uhid" ];
            systemd.services.xivstream-cpu-policy = {
              description = "xivstream: yield game CPUs while the host is busy";
              wantedBy = [ "multi-user.target" ];
              after = [ "incus.service" ];
              wants = [ "incus.service" ];
              path = [ pkgs.incus ];
              environment.XIVSTREAM_CONFIG = toString configFile;
              restartTriggers = [ configFile ];
              serviceConfig = {
                ExecStart = "${pkg}/bin/xivstream cpu-policy";
                Restart = "on-failure";
                RestartSec = 5;
                RuntimeDirectory = "xivstream-cpu-policy";
              };
            };
            systemd.services.xivstream-gpu-share = {
              description = "xivstream: give the game the GPU's memory while it runs";
              wantedBy = [ "multi-user.target" ];
              after = [ "incus.service" ];
              wants = [ "incus.service" ];
              path = [ pkgs.incus pkgs.curl pkgs.procps pkgs.iproute2 ];
              environment.XIVSTREAM_CONFIG = toString configFile;
              serviceConfig = {
                ExecStart = "${pkg}/bin/xivstream gpu-share";
                ExecStopPost = "${pkg}/bin/xivstream gpu-share --stop";
                Restart = "always";
                RestartSec = 5;
                StateDirectory = "xivstream";
              };
            };
            systemd.services.xivstream-container = {
              description = "xivstream: start the game container once its stream address exists";
              wantedBy = [ "multi-user.target" ];
              after = [ "incus.service" "tailscaled.service" "network-online.target" ];
              wants = [ "incus.service" "network-online.target" ];
              path = [ pkgs.incus pkgs.iproute2 ];
              environment.XIVSTREAM_CONFIG = toString configFile;
              serviceConfig = {
                Type = "oneshot";
                RemainAfterExit = true;
                ExecStart = "${pkg}/bin/xivstream start-container";
              };
            };
          };
        };
    };
}
