{
  perSystem = { pkgs, ... }: {
    # Go for the package build and the dev/CI shells. go.mod requires go
    # >= 1.27 (#114) and buildGoModule's default nixpkgs toolchain lags
    # that. It is also pinned to the SAME major that nixpkgs builds
    # golangci-lint with: the linter loads packages through the `go` on
    # PATH, and a mismatch fails every package with
    # `compile: version go1.X does not match go tool version go1.Y`
    # rather than reporting real findings (nixpkgs currently ships
    # golangci-lint built with 1.27 while `pkgs.go` is 1.26). When a
    # nixpkgs bump moves the linter's toolchain, that error is the
    # signal to bump this line.
    _module.args.go = pkgs.go_1_27;
  };
}
