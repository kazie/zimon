# services.zimon: zimon serve as a NixOS service. flake.nix passes `self`,
# for the package.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.zimon;
  toml = pkgs.formats.toml { };
  configFile = toml.generate "zimon-config.toml" settings;
  configPath = "/etc/zimon/config.toml"; # environment.etc."zimon/config.toml"
  libraryFile = toml.generate "zimon-library.toml" cfg.library;
  # library.toml: the module's, read-only, or zimon's own in the data folder.
  libraryPath =
    if cfg.library == null then "${cfg.dataDir}/library.toml" else "/etc/zimon/library.toml";
  settings = lib.recursiveUpdate {
    library_dir = "${cfg.dataDir}/library";
    state_dir = cfg.dataDir;
    library_file = libraryPath;
    serve = {
      address = "all";
      port = 8080;
      kiwix_serve_path = lib.getExe' cfg.kiwixTools "kiwix-serve";
    };
    schedule.update = "weekly";
  } cfg.settings;
  zimon = lib.getExe cfg.package;
  # zimon for the admin's shell: the service's config unless told otherwise,
  # so `sudo -u zimon zimon list` (sudo drops the environment) works.
  adminZimon = pkgs.symlinkJoin {
    name = "zimon-${cfg.package.version or "dev"}";
    paths = [ cfg.package ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postBuild = "wrapProgram $out/bin/zimon --set-default ZIMON_CONFIG ${configPath}";
  };
  # The folders zimon writes: settings may move them out of dataDir. The
  # module's library.toml is in /etc, which zimon only reads.
  writable = lib.unique (
    [
      cfg.dataDir
      settings.library_dir
      settings.state_dir
    ]
    ++ lib.optional (cfg.library == null) (dirOf settings.library_file)
  );
  service = {
    User = cfg.user;
    Group = cfg.group;
    Environment = [ "ZIMON_CONFIG=${configPath}" ];
    ReadWritePaths = writable; # made by tmpfiles, below
    ProtectSystem = "strict";
    # A folder of zimon's under /home must stay reachable.
    ProtectHome = if lib.any (lib.hasPrefix "/home/") writable then "read-only" else true;
    PrivateTmp = true;
    NoNewPrivileges = true;
  };
in
{
  options.services.zimon = {
    enable = lib.mkEnableOption "zimon, which keeps a library of Kiwix ZIM files and serves it";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "zimon's flake package";
      description = "The zimon package.";
    };

    kiwixTools = lib.mkPackageOption pkgs "kiwix-tools" { };

    user = lib.mkOption {
      type = lib.types.str;
      default = "zimon";
      description = "The user zimon runs as; made when it is \"zimon\".";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "zimon";
      description = "The group zimon runs as; made when it is \"zimon\".";
    };

    dataDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/zimon";
      description = "Where the ZIM files (library/), the database and zimon's own library.toml live.";
    };

    settings = lib.mkOption {
      type = toml.type;
      default = { };
      example = lib.literalExpression ''
        {
          languages = [ "eng" "swe" ];
          download.rate_limit = "20MB";
          schedule.time = "04:00";
        }
      '';
      description = ''
        config.toml (see `zimon config --help`), over the module's defaults:
        the data folder, `serve.address = "all"` on port 8080 (the admin page
        on 8081), kiwix-serve from kiwix-tools, and `schedule.update = "weekly"`.
      '';
    };

    library = lib.mkOption {
      type = lib.types.nullOr toml.type;
      default = null;
      example = lib.literalExpression ''
        {
          budget = "250GB";
          collections = { linux = "deep"; japan = "standard"; };
          items."wikipedia_en_all" = "nopic";
        }
      '';
      description = ''
        library.toml: what the library should contain. Set, it is read-only:
        zimon add, remove and level refuse to change it, and
        `zimon apply --yes` runs when it changes. null leaves library.toml to
        zimon, in the data folder.
      '';
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open the books' port (serve.port) in the firewall. The admin port stays closed.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users = lib.mkIf (cfg.user == "zimon") {
      zimon = {
        isSystemUser = true;
        group = cfg.group;
        home = cfg.dataDir;
      };
    };
    users.groups = lib.mkIf (cfg.group == "zimon") { zimon = { }; };

    environment.etc = {
      "zimon/config.toml".source = configFile;
    }
    // lib.optionalAttrs (cfg.library != null) {
      "zimon/library.toml".source = libraryFile;
    };
    # Run it as the service's user: sudo -u zimon zimon list.
    environment.systemPackages = [ adminZimon ];

    systemd.tmpfiles.settings."10-zimon" = lib.genAttrs writable (_: {
      d = {
        inherit (cfg) user group;
        mode = "0750";
      };
    });

    systemd.services.zimon = {
      description = "zimon: Kiwix ZIM files, served";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      restartTriggers = [ configFile ];
      serviceConfig = service // {
        ExecStart = "${zimon} serve --no-color";
        Restart = "on-failure";
      };
    };

    # Makes the disk match library.toml whenever the module's library.toml
    # changes. It takes the state lock, so it waits for a job of zimon serve.
    # Type exec, not oneshot: a switch starts it and goes on, rather than
    # wait for every download (or fail when one does).
    systemd.services.zimon-apply = lib.mkIf (cfg.library != null) {
      description = "zimon: make the library match library.toml";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      restartTriggers = [
        configFile
        libraryFile
      ];
      # The network may not be up yet at boot: a few tries, then it waits
      # for the next switch (or zimon serve's scheduled update, or a hand).
      startLimitIntervalSec = 3600;
      startLimitBurst = 4;
      serviceConfig = service // {
        Type = "exec";
        RemainAfterExit = true;
        ExecStart = "${zimon} apply --yes --no-color";
        Restart = "on-failure";
        RestartSec = "5min";
      };
    };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ settings.serve.port ];
  };
}
