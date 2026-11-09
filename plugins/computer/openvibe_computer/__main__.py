import sys

from openvibe_computer.link_egress import LinkComputer as Computer
from openvibe_plugin import run

sys.exit(run(Computer()))
