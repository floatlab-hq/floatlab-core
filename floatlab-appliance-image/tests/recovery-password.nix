{ pkgs, installed }:
pkgs.testers.runNixOSTest {
  name = "floatlab-recovery-password";
  nodes.host = { ... }: {
    services.openssh = {
      enable = true;
      settings.PermitRootLogin = installed.services.openssh.settings.PermitRootLogin;
      authorizedKeysFiles = installed.services.openssh.authorizedKeysFiles;
      extraConfig = installed.services.openssh.extraConfig;
    };
    users.mutableUsers = installed.users.mutableUsers;
    users.users.root.hashedPasswordFile = installed.users.users.root.hashedPasswordFile;
    users.users.reader.isNormalUser = true;
    environment.systemPackages = [ pkgs.mkpasswd pkgs.pamtester pkgs.openssh pkgs.sshpass ];
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
    host.succeed("ssh-keygen -q -t ed25519 -N \"\" -f /root/test-key; install -m 600 /root/test-key.pub /floatlab/system/etc/root-authorized-keys")
    password_hash = host.succeed("cat /floatlab/system/etc/root-password.hash").strip()
    for _ in range(2):
        host.shutdown()
        host.start()
        host.wait_for_unit("multi-user.target")
        host.wait_for_unit("sshd.service")
        host.wait_for_open_port(22)
        ssh = "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 "
        host.succeed(ssh + "-o BatchMode=yes -i /root/test-key root@localhost true")
        host.fail(ssh + "-o BatchMode=yes -i /root/test-key reader@localhost true")
        host.fail("sshpass -p console-secret " + ssh + "-o PubkeyAuthentication=no -o PreferredAuthentications=password root@localhost true")
        assert host.succeed("stat -c %u:%g:%a /floatlab/system/etc/root-authorized-keys").strip() == "0:0:600"
        assert host.succeed("stat -c %u:%g:%a /floatlab/system/etc/root-password.hash").strip() == "0:0:600"
        host.fail("runuser -u reader -- cat /floatlab/system/etc/root-password.hash")
        assert host.succeed("getent shadow root").split(":")[1] == password_hash
        host.fail("printf '%s\\n' wrong | pamtester login root authenticate")
        host.succeed("printf '%s\\n' console-secret | pamtester login root authenticate acct_mgmt")
  '';
}
