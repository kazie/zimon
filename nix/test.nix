# The NixOS module in a VM: fakekiwix stands in for Kiwix, inside the VM.
{ self, pkgs }:
let
  fakekiwix = self.packages.${pkgs.stdenv.hostPlatform.system}.fakekiwix;
in
pkgs.testers.runNixOSTest {
  name = "zimon";
  nodes.machine = {
    imports = [ self.nixosModules.default ];
    systemd.services.fakekiwix = {
      wantedBy = [ "multi-user.target" ];
      before = [
        "zimon.service"
        "zimon-apply.service"
      ];
      serviceConfig = {
        ExecStart = "${fakekiwix}/bin/fakekiwix -addr 127.0.0.1:8899 -quiet";
        # Started means listening, so zimon-apply never finds the port closed.
        ExecStartPost = "${pkgs.bash}/bin/bash -c 'until (exec 3<>/dev/tcp/127.0.0.1/8899) 2>/dev/null; do sleep 0.1; done'";
      };
    };
    services.zimon = {
      enable = true;
      settings = {
        catalog_url = "http://127.0.0.1:8899/catalog/v2";
        library_dir = "/srv/zims"; # outside dataDir, as on a data disk
      };
      library.items."devdocs_en_bash" = "on";
      openFirewall = true;
    };
    # The same machine, asking for one more ZIM.
    specialisation.more.configuration.services.zimon.library.items."devdocs_en_go" = "on";
  };
  testScript = ''
    # zimon-apply runs on its own: a switch or a boot doesn't wait for it.
    machine.wait_for_unit("zimon-apply.service")
    machine.wait_until_succeeds("test -f /srv/zims/devdocs_en_bash_2026-10.zim")
    machine.wait_for_unit("zimon.service")
    machine.wait_for_open_port(8080)
    machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/status | grep -q '\"ok\":true'")

    # The admin's zimon finds the service's config and library.
    machine.wait_until_succeeds("sudo -u zimon zimon list | grep -q devdocs_en_bash")
    machine.succeed("sudo -u zimon zimon doctor")

    # library.toml is the module's: zimon refuses to change it.
    out = machine.fail("sudo -u zimon zimon add devdocs_en_go --yes 2>&1")
    assert "read-only" in out, out

    # A new library.toml: switching to it makes the disk match.
    machine.succeed("/run/current-system/specialisation/more/bin/switch-to-configuration test")
    machine.wait_until_succeeds("test -f /srv/zims/devdocs_en_go_2026-10.zim")
    machine.wait_until_succeeds("sudo -u zimon zimon list | grep -q devdocs_en_go")
  '';
}
