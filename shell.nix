{ pkgs ? import <nixpkgs> {} }:

with pkgs;

mkShell {
  buildInputs = [
    # gnumake # make
    # python3 # for node-gyp
    go
  ];
}
