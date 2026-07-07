{
  description = "A blazingly fast Slack TUI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
        lib = pkgs.lib;
        lastModifiedDate =
          if self ? lastModifiedDate then self.lastModifiedDate else "19700101";
        shortRev =
          if self ? shortRev then self.shortRev
          else if self ? dirtyShortRev then self.dirtyShortRev
          else "dirty";
        commit =
          if self ? rev then self.rev
          else if self ? dirtyRev then self.dirtyRev
          else shortRev;
        tagVersion =
          if self ? ref
          then lib.match "refs/tags/v(.*)" self.ref
          else null;
        version =
          if tagVersion != null
          then builtins.elemAt tagVersion 0
          else "unstable-${builtins.substring 0 8 lastModifiedDate}-${shortRev}";
        slk = pkgs.buildGo126Module rec {
          pname = "slk";
          inherit version;
          src = ./.;
          vendorHash = "sha256-dPa469oNv6eYyDdly3uhc273DAGz+erc0E3K/am7WoY=";
          subPackages = ["cmd/slk"];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.commit=${commit}"
            "-X main.date=${lastModifiedDate}"
          ];
          checkPhase = ''
            runHook preCheck
            go test ./...
            runHook postCheck
          '';
          meta = {
            description = "A blazingly fast Slack TUI";
            homepage = "https://github.com/gammons/slk";
            license = lib.licenses.mit;
            mainProgram = "slk";
            platforms = lib.platforms.linux ++ lib.platforms.darwin;
          };
        };
      in {
        packages.default = slk;
        packages.slk = slk;
        apps.default = {
          type = "app";
          program = "${slk}/bin/slk";
          meta = slk.meta;
        };
        apps.slk = {
          type = "app";
          program = "${slk}/bin/slk";
          meta = slk.meta;
        };
        checks.default = slk;
      });
}
