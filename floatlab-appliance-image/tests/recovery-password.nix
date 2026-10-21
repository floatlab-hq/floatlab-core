{ pkgs, installed }:
pkgs.testers.runNixOSTest {
  name = "floatlab-recovery-password";
  nodes.host = { ... }: {
    users.mutableUsers = installed.users.mutableUsers;
    users.users.root.hashedPasswordFile = installed.users.users.root.hashedPasswordFile;
    users.users.reader.isNormalUser = true;
    environment.systemPackages = [ pkgs.mkpasswd pkgs.pamtester ];
    virtualisation.emptyDiskImages = [ 128 ];
    virtualisation.fileSystems."/floatlab/system/etc" = {
      device = "/dev/vdb";
      fsType = "ext4";
      autoFormat = true;
      neededForBoot = installed.fileSystems."/floatlab/system/etc".neededForBoot;
    };
  };
  testScript = ''
    host.start()
    host.wait_for_unit("multi-user.target")
    host.succeed("umask 077; mkpasswd -m bcrypt console-secret > /floatlab/system/etc/root-password.hash")
    password_hash = host.succeed("cat /floatlab/system/etc/root-password.hash").strip()
    for _ in range(2):
        host.shutdown()
        host.start()
        host.wait_for_unit("multi-user.target")
        assert host.succeed("stat -c %u:%g:%a /floatlab/system/etc/root-password.hash").strip() == "0:0:600"
        host.fail("runuser -u reader -- cat /floatlab/system/etc/root-password.hash")
        assert host.succeed("getent shadow root").split(":")[1] == password_hash
        host.fail("printf '%s\\n' wrong | pamtester login root authenticate")
        host.succeed("printf '%s\\n' console-secret | pamtester login root authenticate acct_mgmt")
  '';
}
